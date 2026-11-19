package herdr

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// helperEnv marks a process started as the helper. Its value is the process
// id of Herdr's client.
const helperEnv = "SLATE_HERDR_HELPER"

// The helper is this same program, started again. A process has one console,
// and the host needs its own for the pane, so a second process attaches to
// Herdr's. init runs before the host's main, which lets any program that
// imports this package act as the helper.
func init() {
	if v := os.Getenv(helperEnv); v != "" {
		pid, _ := strconv.Atoi(v)
		if err := helper(pid); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

// dial connects to Herdr's API. On Windows the socket is a named pipe named
// after the socket path.
func dial(socket string) (io.ReadWriteCloser, error) {
	return os.OpenFile(`\\.\pipe\`+socket, os.O_RDWR, 0)
}

func startHelper(socket string) (io.WriteCloser, io.Reader, func(), error) {
	pid, err := clientProcess(socket)
	if err != nil {
		return nil, nil, nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, nil, err
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), helperEnv+"="+strconv.Itoa(pid))
	// The helper starts with no console and attaches to Herdr's.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	reports, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	return in, reports, func() { cmd.Wait() }, nil
}

// clientProcess returns the process id of the Herdr client that shows the
// session behind socket. The client is the Herdr process that holds the real
// terminal.
func clientProcess(socket string) (int, error) {
	// A named session keeps its socket in sessions\<name>.
	session := ""
	if dir := filepath.Dir(socket); strings.EqualFold(filepath.Base(filepath.Dir(dir)), "sessions") {
		session = filepath.Base(dir)
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "herdr.exe") {
			continue
		}
		args, err := commandLine(entry.ProcessID)
		if err != nil || len(args) == 0 {
			continue
		}
		if name, ok := clientSession(args[1:]); ok && name == session {
			return int(entry.ProcessID), nil
		}
	}
	return 0, errors.New("found no Herdr client process for this session")
}

// clientSession reports whether a Herdr command line starts a client, and
// for which session. The default session has the empty name.
func clientSession(args []string) (session string, ok bool) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--session" && i+1 < len(args):
			session = args[i+1]
			i++
		case a == "session" && i+2 < len(args) && args[i+1] == "attach":
			return args[i+2], true
		case strings.HasPrefix(a, "--"):
		default:
			return "", false // a subcommand, such as server or pane
		}
	}
	return session, true
}

// commandLine returns the arguments another process was started with.
func commandLine(pid uint32) ([]string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	const processCommandLineInformation = 60
	buf := make([]byte, 8192)
	var n uint32
	if err := windows.NtQueryInformationProcess(h, processCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &n); err != nil {
		return nil, err
	}
	s := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
	return windows.DecomposeCommandLine(s.String())
}

var (
	kernel32            = windows.NewLazySystemDLL("kernel32.dll")
	attachConsole       = kernel32.NewProc("AttachConsole")
	readConsoleOutput   = kernel32.NewProc("ReadConsoleOutputW")
	getConsoleWindow    = kernel32.NewProc("GetConsoleWindow")
	user32              = windows.NewLazySystemDLL("user32.dll")
	getAncestor         = user32.NewProc("GetAncestor")
	getForegroundWindow = user32.NewProc("GetForegroundWindow")
)

// A cell of the console as ReadConsoleOutput returns it.
type charInfo struct{ char, attr uint16 }

const (
	plain  = 0x07 // grey on black, the console's default
	marked = 0x01 // blue on black, which SGR 34 gives; a space shows neither

	lookEvery   = 4 * time.Millisecond
	reportEvery = 500 * time.Millisecond

	// With redraw_on_focus_gained on, which is its default, Herdr repaints
	// the pane 50 to 60 ms after its window comes to the front. For hotFor
	// after that the helper looks every hotEvery, to put the image back as
	// soon as Herdr has finished.
	hotEvery = time.Millisecond
	hotFor   = 150 * time.Millisecond
)

// logEnv names a file in which the helper notes what it sees in the marked
// cells, for finding out why a block stayed empty.
const logEnv = "SLATE_HERDR_LOG"

// console is the helper's hold on the outer terminal.
type console struct {
	out windows.Handle

	mu         sync.Mutex
	x, y, w, h int        // the marked cells; w is 0 when there are none
	image      []byte     // the whole image last drawn over them
	cells      []charInfo // what the last look read
	unmarked   int        // cells found without their mark at the last look, or -1
	told       bool       // the host knows of the wipe now showing
	reported   time.Time  // when the host was last told of a wipe
	state      string     // the last line written to the log
	log        *os.File
	looked     time.Time // when the cells were last looked at; used by watch alone
}

// helper attaches to the console of process pid and carries out the
// messages it reads from standard input until that closes.
func helper(pid int) error {
	if ok, _, err := attachConsole.Call(uintptr(pid)); ok == 0 {
		return err
	}
	name, _ := syscall.UTF16PtrFromString("CONOUT$")
	out, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	c := &console{out: out, unmarked: -1}
	if path := os.Getenv(logEnv); path != "" {
		c.log, _ = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	}
	go c.watch()
	var head [5]byte
	for {
		if _, err := io.ReadFull(os.Stdin, head[:]); err != nil {
			return nil // the host closed the pipe or exited
		}
		payload := make([]byte, binary.LittleEndian.Uint32(head[1:]))
		if _, err := io.ReadFull(os.Stdin, payload); err != nil {
			return nil
		}
		v := func(i int) int { return int(int32(binary.LittleEndian.Uint32(payload[i*4:]))) }
		c.mu.Lock()
		switch {
		case head[0] == 'W':
			c.write(payload)
		case head[0] == 'K':
			// The whole image over itself: the cells keep their marks.
			if c.w > 0 {
				c.image = payload
			}
			c.write(payload)
		case head[0] == 'F' && len(payload) >= 16:
			c.x, c.y, c.w, c.h = v(0), v(1), v(2), v(3)
			c.unmarked = -1
			c.image = payload[16:]
			c.note("draw %d bytes at %d,%d %dx%d", len(c.image), c.x, c.y, c.w, c.h)
			c.repaint()
		case head[0] == 'E' && len(payload) == 16:
			c.w = 0
			c.note("erase")
			c.write(c.eraseBlank(v(0), v(1), v(2), v(3)))
		}
		c.mu.Unlock()
	}
}

// terminalWindow returns the window that shows the attached console, or 0.
// A console behind a pseudoconsole has a hidden window of its own, which the
// terminal owns.
func terminalWindow() uintptr {
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
		return 0
	}
	const rootOwner = 3 // GA_ROOTOWNER
	root, _, _ := getAncestor.Call(hwnd, rootOwner)
	if root == hwnd {
		return 0
	}
	return root
}

// watch looks at the marked cells until the process ends and tells the host
// of each wipe. It looks more often just after the terminal's window comes
// to the front.
func (c *console) watch() {
	window := terminalWindow()
	c.mu.Lock()
	c.note("terminal window %#x", window)
	c.mu.Unlock()
	var front bool
	var hotUntil time.Time
	for {
		now := time.Now()
		if window != 0 {
			fg, _, _ := getForegroundWindow.Call()
			if fg == window && !front {
				hotUntil = now.Add(hotFor)
				c.mu.Lock()
				c.note("window to the front")
				c.mu.Unlock()
			}
			front = fg == window
		}
		hot := now.Before(hotUntil)
		if hot || now.Sub(c.looked) >= lookEvery {
			c.looked = now
			if c.wiped() {
				os.Stdout.Write([]byte{'w'})
			}
		}
		// The window is checked every millisecond, which costs next to
		// nothing. The cells are read that often only while hot.
		time.Sleep(hotEvery)
	}
}

func (c *console) write(p []byte) {
	for len(p) > 0 {
		var n uint32
		if windows.WriteFile(c.out, p, &n, nil) != nil {
			return
		}
		p = p[n:]
	}
}

// read fills cells with the w by h console cells at column x, row y, and
// returns how many rows it got.
func (c *console) read(x, y, w, h int, cells []charInfo) int {
	region := windows.SmallRect{Left: int16(x), Top: int16(y), Right: int16(x + w - 1), Bottom: int16(y + h - 1)}
	size := uintptr(uint16(w)) | uintptr(uint16(h))<<16 // a COORD, passed by value
	ok, _, _ := readConsoleOutput.Call(uintptr(c.out), uintptr(unsafe.Pointer(&cells[0])), size, 0,
		uintptr(unsafe.Pointer(&region)))
	if ok == 0 || int(region.Right-region.Left)+1 != w {
		return 0
	}
	return int(region.Bottom-region.Top) + 1
}

// repaint writes the marks and the image over them. The marks go first: a
// space written to a cell removes the image there. One write keeps the block
// from showing empty in between.
func (c *console) repaint() {
	seq := []byte("\x1b7\x1b[34m") // DECSC keeps Herdr's cursor and colours
	for r := 0; r < c.h; r++ {
		seq = append(seq, fmt.Sprintf("\x1b[%d;%dH%s", c.y+r+1, c.x+1, strings.Repeat(" ", c.w))...)
	}
	seq = append(seq, "\x1b[m\x1b8"...)
	c.write(append(seq, c.image...))
}

// note writes a line to the log when it differs from the last one.
func (c *console) note(format string, a ...any) {
	if c.log == nil {
		return
	}
	if line := fmt.Sprintf(format, a...); line != c.state {
		c.state = line
		fmt.Fprintln(c.log, time.Now().Format("15:04:05.000"), line)
	}
}

// wiped looks at the marked cells. When Herdr has written over some of them
// and all of those are blank, the image is gone: wiped writes it again as
// it last was and reports that the host should send the current one. While
// any holds text, something of Herdr's covers the block, and an image drawn
// now would cover that in turn.
func (c *console) wiped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.w < 1 || c.h < 1 {
		return false
	}
	if len(c.cells) != c.w*c.h {
		c.cells = make([]charInfo, c.w*c.h)
	}
	rows := c.read(c.x, c.y, c.w, c.h, c.cells)
	if rows == 0 {
		c.note("cannot read the cells")
		return false
	}
	unmarked := 0
	for i, cell := range c.cells[:rows*c.w] {
		switch {
		case cell.char == ' ' && cell.attr == marked:
		case cell.char == ' ' && cell.attr == plain:
			unmarked++
		default:
			c.note("covered: %q with attributes %#x at %d,%d", rune(cell.char), cell.attr, i%c.w, i/c.w)
			c.unmarked = -1
			return false
		}
	}
	if unmarked == 0 {
		c.note("marked")
		c.unmarked, c.told = 0, false
		return false
	}
	// Two looks that agree mean Herdr has finished repainting. A write in
	// the middle of its output could break one of its escape sequences.
	settled := unmarked == c.unmarked
	c.unmarked = unmarked
	if !settled {
		c.note("wiping: %d of %d cells", unmarked, len(c.cells))
	}
	if !settled || (c.told && time.Since(c.reported) < reportEvery) {
		return false
	}
	c.note("wiped: %d of %d cells", unmarked, len(c.cells))
	c.repaint()
	c.told, c.reported = true, time.Now()
	return true
}

// eraseBlank reads the console's cells in the rectangle and returns the
// sequences that write a space over each run of plain or marked spaces. A
// space written over a cell removes the image there and the mark.
func (c *console) eraseBlank(x, y, w, h int) []byte {
	blank := func(cell charInfo) bool {
		return cell.char == ' ' && (cell.attr == plain || cell.attr == marked)
	}
	var seq []byte
	row := make([]charInfo, w)
	for r := 0; r < h; r++ {
		got := w * c.read(x, y+r, w, 1, row)
		for i := 0; i < got; {
			if !blank(row[i]) {
				i++
				continue
			}
			end := i
			for end < got && blank(row[end]) {
				end++
			}
			if seq == nil {
				seq = append(seq, "\x1b7\x1b[m"...) // DECSC keeps Herdr's cursor and colours
			}
			seq = append(seq, fmt.Sprintf("\x1b[%d;%dH%s", y+r+1, x+i+1, strings.Repeat(" ", end-i))...)
			i = end
		}
	}
	if seq != nil {
		seq = append(seq, "\x1b8"...)
	}
	return seq
}
