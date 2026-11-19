//go:build windows

// slate-probe tests whether an image can reach the outer terminal from under
// a multiplexer. It attaches to the console of another process (the
// multiplexer's client, which owns the real terminal) and writes one Sixel
// test image there at a given cell.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/lawsmd/slate/sixel"
	"github.com/lawsmd/slate/term"
)

var (
	kernel32      = windows.NewLazySystemDLL("kernel32.dll")
	freeConsole   = kernel32.NewProc("FreeConsole")
	attachConsole = kernel32.NewProc("AttachConsole")

	readConsoleOutput = kernel32.NewProc("ReadConsoleOutputW")
	writeConsoleInput = kernel32.NewProc("WriteConsoleInputW")

	getCurrentConsoleFont = kernel32.NewProc("GetCurrentConsoleFont")
	getConsoleWindow      = kernel32.NewProc("GetConsoleWindow")
	user32                = windows.NewLazySystemDLL("user32.dll")
	getClientRect         = user32.NewProc("GetClientRect")
	getAncestor           = user32.NewProc("GetAncestor")
)

func main() {
	pid := flag.Int("pid", 0, "process whose console to write to")
	col := flag.Int("col", 1, "left cell, counted from 1")
	row := flag.Int("row", 1, "top cell, counted from 1")
	w := flag.Int("w", 300, "image width in pixels")
	h := flag.Int("h", 120, "image height in pixels")
	logPath := flag.String("log", "", "file for the report; the probe has no console of its own")
	raw := flag.String("raw", "", `bytes to write in place of the image, with Go escapes such as \x1b`)
	api := flag.String("api", "", "a JSON request to send to the socket at HERDR_SOCKET_PATH")
	listen := flag.Duration("listen", time.Second, "how long to read replies to -api")
	focus := flag.Duration("focus", 0, "with -pid: tell that console it lost focus, and after this long that it has it again")
	cellsFlag := flag.String("cells", "", `with -pid: print the characters and attributes of a rectangle of cells at -col and -row, as "40x5"`)
	queries := flag.Bool("queries", false, "report what this terminal answers to size queries, then exit")
	flag.Parse()

	report := os.Stdout
	if *logPath != "" {
		f, err := os.Create(*logPath)
		if err != nil {
			os.Exit(2)
		}
		defer f.Close()
		report = f
	}
	if *api != "" {
		// Send one request line to Herdr's socket and print what comes back.
		// On Windows the socket is a named pipe named after the socket path.
		conn, err := os.OpenFile(`\\.\pipe\`+os.Getenv("HERDR_SOCKET_PATH"), os.O_RDWR, 0)
		if err != nil {
			fmt.Fprintln(report, "error:", err)
			os.Exit(1)
		}
		fmt.Fprintln(conn, *api)
		go io.Copy(report, conn)
		time.Sleep(*listen)
		return
	}
	if *queries {
		// What this pane's own terminal answers, asked before leaving its console.
		t, err := term.Open()
		if err != nil {
			fmt.Fprintln(report, "error:", err)
			os.Exit(1)
		}
		for _, q := range []struct {
			name, request string
			final         byte
		}{
			{"cell size (16t)", "\x1b[16t", 't'}, {"text area pixels (14t)", "\x1b[14t", 't'},
			{"text area cells (18t)", "\x1b[18t", 't'}, {"device attributes", "\x1b[c", 'c'},
		} {
			reply, ok := t.Query(q.request, q.final, time.Second)
			fmt.Fprintf(report, "%s: %q answered=%v\n", q.name, reply, ok)
		}
		t.Close()
		return
	}
	if *focus > 0 {
		if err := refocus(*pid, *focus); err != nil {
			fmt.Fprintln(report, "error:", err)
			os.Exit(1)
		}
		return
	}
	if *cellsFlag != "" {
		var cw, ch int
		fmt.Sscanf(*cellsFlag, "%dx%d", &cw, &ch)
		if err := readCells(report, *pid, *col-1, *row-1, cw, ch); err != nil {
			fmt.Fprintln(report, "error:", err)
			os.Exit(1)
		}
		return
	}
	rawSeq, _ := strconv.Unquote(`"` + *raw + `"`)
	if err := probe(report, *pid, *col, *row, *w, *h, rawSeq); err != nil {
		fmt.Fprintln(report, "error:", err)
		os.Exit(1)
	}
}

// refocus writes the two input records a console gets when its window loses
// the keyboard focus and gets it back. It shows what a multiplexer does on
// alt+tab without touching the real focus.
func refocus(pid int, away time.Duration) error {
	freeConsole.Call()
	if ok, _, err := attachConsole.Call(uintptr(pid)); ok == 0 {
		return fmt.Errorf("AttachConsole(%d): %w", pid, err)
	}
	name, _ := syscall.UTF16PtrFromString("CONIN$")
	in, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return fmt.Errorf("open CONIN$: %w", err)
	}
	defer windows.CloseHandle(in)
	send := func(focused uint32) error {
		const focusEvent = 0x10
		rec := struct { // INPUT_RECORD holding a FOCUS_EVENT_RECORD
			eventType uint16
			_         uint16
			setFocus  uint32
			_         [12]byte
		}{eventType: focusEvent, setFocus: focused}
		var n uint32
		if ok, _, err := writeConsoleInput.Call(uintptr(in), uintptr(unsafe.Pointer(&rec)), 1, uintptr(unsafe.Pointer(&n))); ok == 0 {
			return fmt.Errorf("WriteConsoleInput: %w", err)
		}
		return nil
	}
	if err := send(0); err != nil {
		return err
	}
	time.Sleep(away)
	return send(1)
}

// readCells prints what the console holds in a rectangle of cells: per row,
// the text and each run of equal attributes.
func readCells(report *os.File, pid, x, y, w, h int) error {
	freeConsole.Call()
	if ok, _, err := attachConsole.Call(uintptr(pid)); ok == 0 {
		return fmt.Errorf("AttachConsole(%d): %w", pid, err)
	}
	name, _ := syscall.UTF16PtrFromString("CONOUT$")
	out, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return fmt.Errorf("open CONOUT$: %w", err)
	}
	defer windows.CloseHandle(out)
	type charInfo struct{ char, attr uint16 }
	row := make([]charInfo, w)
	for r := 0; r < h; r++ {
		region := windows.SmallRect{Left: int16(x), Top: int16(y + r), Right: int16(x + w - 1), Bottom: int16(y + r)}
		size := uintptr(uint16(w)) | 1<<16
		if ok, _, err := readConsoleOutput.Call(uintptr(out), uintptr(unsafe.Pointer(&row[0])), size, 0,
			uintptr(unsafe.Pointer(&region))); ok == 0 {
			return fmt.Errorf("ReadConsoleOutput: %w", err)
		}
		text := make([]rune, w)
		attrs := ""
		for c := range row {
			text[c] = rune(row[c].char)
			if c == 0 || row[c].attr != row[c-1].attr {
				attrs += fmt.Sprintf(" %d:%#x", c, row[c].attr)
			}
		}
		fmt.Fprintf(report, "%q%s\n", string(text), attrs)
	}
	return nil
}

type rect struct{ l, t, r, b int32 }

func probe(report *os.File, pid, col, row, w, h int, raw string) error {
	freeConsole.Call()
	if ok, _, err := attachConsole.Call(uintptr(pid)); ok == 0 {
		return fmt.Errorf("AttachConsole(%d): %w", pid, err)
	}
	name, _ := syscall.UTF16PtrFromString("CONOUT$")
	out, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return fmt.Errorf("open CONOUT$: %w", err)
	}
	defer windows.CloseHandle(out)

	var mode uint32
	windows.GetConsoleMode(out, &mode)
	var info windows.ConsoleScreenBufferInfo
	windows.GetConsoleScreenBufferInfo(out, &info)
	fmt.Fprintf(report, "mode %#x (vt %v), buffer %dx%d, window %dx%d\n", mode,
		mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0, info.Size.X, info.Size.Y,
		info.Window.Right-info.Window.Left+1, info.Window.Bottom-info.Window.Top+1)

	var font struct { // CONSOLE_FONT_INFO: what the console claims its font is
		Index    uint32
		FontSize windows.Coord
	}
	getCurrentConsoleFont.Call(uintptr(out), 0, uintptr(unsafe.Pointer(&font)))
	hwnd, _, _ := getConsoleWindow.Call()
	var own, root rect
	getClientRect.Call(hwnd, uintptr(unsafe.Pointer(&own)))
	owner, _, _ := getAncestor.Call(hwnd, 3) // GA_ROOTOWNER
	getClientRect.Call(owner, uintptr(unsafe.Pointer(&root)))
	fmt.Fprintf(report, "console font %dx%d, console window %#x client %dx%d, root owner %#x client %dx%d\n",
		font.FontSize.X, font.FontSize.Y, hwnd, own.r-own.l, own.b-own.t, owner, root.r-root.l, root.b-root.t)

	if raw != "" {
		var n uint32
		err := windows.WriteFile(out, []byte(raw), &n, nil)
		fmt.Fprintf(report, "wrote raw %q: %v\n", raw, err)
		return err
	}

	// A gradient with a white frame, so that position and size show.
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := pix[(y*w+x)*4:]
			p[0], p[1], p[2] = uint8(x*255/w), uint8(y*255/h), 160
			if x < 3 || y < 3 || x >= w-3 || y >= h-3 {
				p[0], p[1], p[2] = 255, 255, 255
			}
		}
	}
	var enc sixel.Encoder
	seq := []byte(fmt.Sprintf("\x1b7\x1b[%d;%dH", row, col))
	seq = enc.Encode(seq, pix, w*4, w, h)
	seq = append(seq, "\x1b8"...)
	for p := seq; len(p) > 0; {
		var n uint32
		if err := windows.WriteFile(out, p, &n, nil); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		p = p[n:]
	}
	fmt.Fprintf(report, "wrote %d bytes at row %d, col %d\n", len(seq), row, col)
	return nil
}
