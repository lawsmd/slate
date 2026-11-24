package browser

import (
	"fmt"
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Explorer makes one window of this class for each tab that Edge asks it to
// list in the Alt-Tab switcher, and gives it the title of the tab.
const tabProxyClass = "Windows.Internal.Shell.TabProxyWindow"

var (
	user32         = windows.NewLazySystemDLL("user32.dll")
	findWindowEx   = user32.NewProc("FindWindowExW")
	getWindowTextW = user32.NewProc("GetWindowTextW")
)

// tabProxies counts the tab windows of Explorer that have this title.
func tabProxies(title string) int {
	class, _ := windows.UTF16PtrFromString(tabProxyClass)
	n := 0
	var h uintptr
	for {
		h, _, _ = findWindowEx.Call(0, h, uintptr(unsafe.Pointer(class)), 0)
		if h == 0 {
			return n
		}
		buf := make([]uint16, 512)
		getWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if windows.UTF16ToString(buf) == title {
			n++
		}
	}
}

// Explorer keeps the Alt-Tab entry of a tab after a headless Edge exits, and
// the user can neither switch to the entry nor close it. Edge asks for the
// entry about a second after the page has its title.
func TestPageIsNotListedInAltTab(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := Find(); err != nil {
		t.Skip(err)
	}
	b, err := Launch(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	session, err := b.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	title := fmt.Sprintf("slate-alt-tab-test-%d", os.Getpid())
	url := "data:text/html,<title>" + title + "</title>"
	if err := b.Conn.Call(session, "Page.navigate", map[string]any{"url": url}, nil); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if n := tabProxies(title); n != 0 {
			t.Fatalf("Explorer lists the page in Alt-Tab: %d window(s) of class %s titled %q", n, tabProxyClass, title)
		}
	}
}
