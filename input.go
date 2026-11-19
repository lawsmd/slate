package slate

import (
	"context"
	"time"
	"unicode"
	"unicode/utf8"
)

// Mod is a set of modifier keys.
type Mod uint8

// The values match the DevTools protocol's modifier bits.
const (
	Alt Mod = 1 << iota
	Ctrl
	Meta
	Shift
)

// Key is one key press.
type Key struct {
	// Name is the key's DOM name: "Enter", "Backspace", "ArrowLeft", or the
	// character itself, such as "a".
	Name string
	// Text is what the key types, if anything. Empty means Name when Name
	// is a single character and neither Ctrl nor Alt is held.
	Text string
	Mod  Mod
}

var keyCodes = map[string]int{
	"Backspace": 8, "Tab": 9, "Enter": 13, "Escape": 27, " ": 32,
	"PageUp": 33, "PageDown": 34, "End": 35, "Home": 36,
	"ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40,
	"Insert": 45, "Delete": 46,
	"F1": 112, "F2": 113, "F3": 114, "F4": 115, "F5": 116, "F6": 117,
	"F7": 118, "F8": 119, "F9": 120, "F10": 121, "F11": 122, "F12": 123,
}

// Key sends a key press to the editor.
func (e *Editor) Key(k Key) {
	down := map[string]any{"type": "rawKeyDown", "key": k.Name, "modifiers": int(k.Mod)}
	up := map[string]any{"type": "keyUp", "key": k.Name, "modifiers": int(k.Mod)}

	text := k.Text
	single := utf8.RuneCountInString(k.Name) == 1
	if text == "" && single && k.Mod&(Ctrl|Alt|Meta) == 0 {
		text = k.Name
	}
	if k.Name == "Enter" && text == "" {
		text = "\r"
	}

	code, vk := k.Name, keyCodes[k.Name]
	if single {
		r, _ := utf8.DecodeRuneInString(k.Name)
		switch {
		case r == ' ':
			code = "Space"
		case unicode.IsLetter(r) && r < 128:
			vk = int(unicode.ToUpper(r))
			code = "Key" + string(unicode.ToUpper(r))
		case r >= '0' && r <= '9':
			vk = int(r)
			code = "Digit" + string(r)
		default:
			code = ""
		}
	}
	for _, m := range []map[string]any{down, up} {
		if code != "" {
			m["code"] = code
		}
		if vk != 0 {
			m["windowsVirtualKeyCode"] = vk
		}
	}
	if text != "" {
		down["type"] = "keyDown"
		down["text"] = text
	}
	e.post("Key", func(pg *page) error {
		// Waiting for the answer keeps the next call behind this key.
		if err := pg.conn.Call(pg.sess, "Input.dispatchKeyEvent", down, nil); err != nil {
			return err
		}
		return pg.conn.Send(pg.sess, "Input.dispatchKeyEvent", up)
	})
}

// MouseAction is what the mouse did.
type MouseAction int

const (
	Press MouseAction = iota
	Release
	Motion
	WheelUp
	WheelDown
)

// MouseButton is the button a mouse event is about. For Motion it is the
// button held down, if any.
type MouseButton int

const (
	NoButton MouseButton = iota
	LeftButton
	MiddleButton
	RightButton
)

// Mouse is one mouse event. Col and Row are the cell inside the block,
// counted from 0 at its top-left corner.
type Mouse struct {
	Col, Row int
	Button   MouseButton
	Action   MouseAction
	Mod      Mod
}

type clickState struct {
	at       time.Time
	col, row int
	button   MouseButton
	count    int
}

const (
	doubleClick = 400 * time.Millisecond
	wheelStep   = 60 // pixels scrolled per wheel notch
)

var buttonNames = [...]string{"none", "left", "middle", "right"}
var buttonBits = [...]int{0, 1, 4, 2}

// Mouse sends a mouse event to the editor. A terminal reports the mouse per
// cell, so the event is placed at the centre of the cell.
func (e *Editor) Mouse(m Mouse) {
	if m.Button < NoButton || m.Button > RightButton {
		return
	}
	e.mu.Lock()
	x := m.Col*e.cellW + e.cellW/2
	y := m.Row*e.cellH + e.cellH/2
	if m.Action == Press {
		c := &e.click
		now := time.Now()
		if c.button == m.Button && c.col == m.Col && c.row == m.Row && now.Sub(c.at) < doubleClick && c.count < 3 {
			c.count++
		} else {
			c.count = 1
		}
		c.at, c.col, c.row, c.button = now, m.Col, m.Row, m.Button
	}
	clicks := e.click.count
	e.mu.Unlock()

	params := map[string]any{"x": x, "y": y, "modifiers": int(m.Mod), "button": buttonNames[m.Button]}
	switch m.Action {
	case Press:
		params["type"], params["clickCount"], params["buttons"] = "mousePressed", clicks, buttonBits[m.Button]
	case Release:
		params["type"], params["clickCount"] = "mouseReleased", clicks
	case Motion:
		params["type"], params["buttons"] = "mouseMoved", buttonBits[m.Button]
	case WheelUp:
		params["type"], params["button"], params["deltaX"], params["deltaY"] = "mouseWheel", "none", 0, -wheelStep
	case WheelDown:
		params["type"], params["button"], params["deltaX"], params["deltaY"] = "mouseWheel", "none", 0, wheelStep
	default:
		return
	}
	e.post("Mouse", func(pg *page) error {
		return pg.conn.Send(pg.sess, "Input.dispatchMouseEvent", params)
	})
}

// Paste inserts text at the cursor, replacing the selection.
func (e *Editor) Paste(text string) {
	e.post("Paste", func(pg *page) error { return e.eval(pg, nil, "slate.paste", text) })
}

// Copy returns the selected text. The terminal cannot select text inside the
// block, so the host puts this on the clipboard itself.
func (e *Editor) Copy(ctx context.Context) (string, error) {
	return get[string](ctx, e, "slate.selection", false)
}

// Cut returns the selected text and removes it from the document.
func (e *Editor) Cut(ctx context.Context) (string, error) {
	return get[string](ctx, e, "slate.selection", true)
}

// Selected takes the selected text as Copy does, or as Cut does with cut
// set, without waiting: the text arrives on the returned channel, empty
// when there is none. The selection is read in the order of the calls, so
// keys sent after Selected do not change what it returns.
func (e *Editor) Selected(cut bool) <-chan string {
	out := make(chan string, 1)
	run := func() {
		var s string
		e.eval(e.pg.Load(), &s, "slate.selection", cut)
		out <- s
	}
	select {
	case e.queue <- run:
	case <-e.done:
		out <- ""
	}
	return out
}
