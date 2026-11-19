package term

import (
	"os"

	"golang.org/x/sys/windows"
)

const utf8CodePage = 65001

// enableVT makes the console interpret escape sequences written to out.
func enableVT(out *os.File) (undo func(), err error) {
	h := windows.Handle(out.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return nil, err
	}
	want := mode | windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING |
		windows.DISABLE_NEWLINE_AUTO_RETURN
	if err := windows.SetConsoleMode(h, want); err != nil {
		return nil, err
	}
	codePage, _ := windows.GetConsoleOutputCP()
	windows.SetConsoleOutputCP(utf8CodePage)
	return func() {
		windows.SetConsoleMode(h, mode)
		if codePage != 0 {
			windows.SetConsoleOutputCP(codePage)
		}
	}, nil
}

// rawWrite writes bytes with WriteFile. os.File.Write on a console converts
// to UTF-16 and calls WriteConsoleW in small pieces, which is slow for an
// image of several hundred kilobytes.
func rawWrite(out *os.File, p []byte) (int, error) {
	h := windows.Handle(out.Fd())
	total := 0
	for len(p) > 0 {
		var n uint32
		if err := windows.WriteFile(h, p, &n, nil); err != nil {
			return total, err
		}
		total += int(n)
		p = p[n:]
	}
	return total, nil
}
