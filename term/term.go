// Package term puts the terminal in raw mode, reads keys and query replies
// from it, and writes bytes to it without translation.
package term

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	xterm "golang.org/x/term"
)

// Kind says what an Event is.
type Kind int

const (
	Text Kind = iota // one typed character, in Event.Rune
	Enter
	Backspace
	Tab
	Left
	Right
	Up
	Down
	Home
	End
	Delete
	CtrlC
)

// Event is one key press.
type Event struct {
	Kind Kind
	Rune rune
}

// Term is the controlling terminal in raw mode.
type Term struct {
	// Events delivers key presses. It is closed when input ends.
	Events chan Event

	in, out *os.File
	restore []func()

	mu      sync.Mutex
	waiters map[byte]chan string
}

// IsTerminal reports whether both standard input and output are a terminal.
func IsTerminal() bool {
	return xterm.IsTerminal(int(os.Stdin.Fd())) && xterm.IsTerminal(int(os.Stdout.Fd()))
}

// Open puts the terminal in raw mode and starts reading input.
func Open() (*Term, error) {
	if !IsTerminal() {
		return nil, errors.New("standard input and output are not a terminal")
	}
	t := &Term{
		Events:  make(chan Event, 1024),
		in:      os.Stdin,
		out:     os.Stdout,
		waiters: map[byte]chan string{},
	}
	state, err := xterm.MakeRaw(int(t.in.Fd()))
	if err != nil {
		return nil, err
	}
	t.restore = append(t.restore, func() { xterm.Restore(int(t.in.Fd()), state) })
	undo, err := enableVT(t.out)
	if err != nil {
		t.Close()
		return nil, err
	}
	t.restore = append(t.restore, undo)
	go t.read()
	return t, nil
}

// Close restores the terminal modes that Open changed.
func (t *Term) Close() {
	for i := len(t.restore) - 1; i >= 0; i-- {
		t.restore[i]()
	}
	t.restore = nil
}

// Write sends p to the terminal unchanged.
func (t *Term) Write(p []byte) (int, error) { return rawWrite(t.out, p) }

// Size returns the terminal's size in cells.
func (t *Term) Size() (cols, rows int) {
	cols, rows, err := xterm.GetSize(int(t.out.Fd()))
	if err != nil {
		return 80, 24
	}
	return cols, rows
}

// Query writes a request and waits for the reply, a control sequence ending
// in final. It returns the reply's parameter text. The terminal answers
// requests in order, so a reply also shows that everything written before the
// request has been read.
func (t *Term) Query(request string, final byte, timeout time.Duration) (string, bool) {
	ch := make(chan string, 1)
	t.mu.Lock()
	t.waiters[final] = ch
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.waiters, final)
		t.mu.Unlock()
	}()
	if _, err := t.Write([]byte(request)); err != nil {
		return "", false
	}
	select {
	case s := <-ch:
		return s, true
	case <-time.After(timeout):
		return "", false
	}
}

// CellSize asks the terminal for the pixel size of one character cell.
func (t *Term) CellSize() (w, h int, ok bool) {
	reply, ok := t.Query("\x1b[16t", 't', time.Second)
	if !ok {
		return 0, 0, false
	}
	parts := strings.Split(reply, ";") // 6 ; height ; width
	if len(parts) != 3 || parts[0] != "6" {
		return 0, 0, false
	}
	h, _ = strconv.Atoi(parts[1])
	w, _ = strconv.Atoi(parts[2])
	return w, h, w > 0 && h > 0
}

// DeviceAttributes returns the terminal's primary device attributes and
// whether they list Sixel support (attribute 4).
func (t *Term) DeviceAttributes() (attrs string, sixel bool) {
	reply, ok := t.Query("\x1b[c", 'c', time.Second)
	if !ok {
		return "", false
	}
	for _, p := range strings.Split(strings.TrimPrefix(reply, "?"), ";") {
		if p == "4" {
			sixel = true
		}
	}
	return reply, sixel
}

func (t *Term) read() {
	defer close(t.Events)
	buf := make([]byte, 4096)
	var pending []byte
	for {
		n, err := t.in.Read(buf)
		if err != nil {
			return
		}
		pending = t.parse(append(pending, buf[:n]...))
	}
}

// parse consumes the complete keys and sequences in p and returns the
// incomplete tail.
func (t *Term) parse(p []byte) []byte {
	for len(p) > 0 {
		c := p[0]
		switch {
		case c == 0x1b:
			if len(p) < 2 {
				return p
			}
			if p[1] == 'O' { // SS3: a function or keypad key
				if len(p) < 3 {
					return p
				}
				p = p[3:]
				continue
			}
			if p[1] != '[' {
				p = p[2:]
				continue
			}
			i := 2
			for i < len(p) && (p[i] < 0x40 || p[i] > 0x7e) {
				i++
			}
			if i == len(p) {
				return p
			}
			t.sequence(string(p[2:i]), p[i])
			p = p[i+1:]
		case c == '\r':
			t.Events <- Event{Kind: Enter}
			p = p[1:]
		case c == 0x7f || c == 0x08:
			t.Events <- Event{Kind: Backspace}
			p = p[1:]
		case c == '\t':
			t.Events <- Event{Kind: Tab}
			p = p[1:]
		case c == 0x03:
			t.Events <- Event{Kind: CtrlC}
			p = p[1:]
		case c < 0x20:
			p = p[1:]
		default:
			if !utf8.FullRune(p) {
				return p
			}
			r, size := utf8.DecodeRune(p)
			t.Events <- Event{Kind: Text, Rune: r}
			p = p[size:]
		}
	}
	return nil
}

func (t *Term) sequence(params string, final byte) {
	t.mu.Lock()
	ch := t.waiters[final]
	t.mu.Unlock()
	if ch != nil {
		select {
		case ch <- params:
		default:
		}
		return
	}
	switch final {
	case 'A':
		t.Events <- Event{Kind: Up}
	case 'B':
		t.Events <- Event{Kind: Down}
	case 'C':
		t.Events <- Event{Kind: Right}
	case 'D':
		t.Events <- Event{Kind: Left}
	case 'H':
		t.Events <- Event{Kind: Home}
	case 'F':
		t.Events <- Event{Kind: End}
	case '~':
		if params == "3" {
			t.Events <- Event{Kind: Delete}
		}
	}
}
