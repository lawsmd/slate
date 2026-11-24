// Package browser launches a headless Chromium and talks to it over the
// DevTools debugging pipe. The pipe is private to the two processes, where a
// debugging port would be open to every local program.
package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Options configures Launch.
type Options struct {
	// Path is the browser executable. Empty means look for Edge, then Chrome.
	Path string
	// GPU leaves GPU compositing on. Software compositing is the default
	// because a screenshot of a GPU frame needs a read-back from the card.
	GPU bool
	// Args are extra command-line switches.
	Args []string
}

// Browser is one running headless browser.
type Browser struct {
	Conn *Conn
	Path string

	cmd *exec.Cmd
	dir string
}

// Find returns the path of an installed Edge or Chrome.
func Find() (string, error) {
	var candidates []string
	for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LocalAppData")} {
		if root == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	for _, name := range []string{"msedge", "microsoft-edge", "google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no Edge or Chrome found; pass the path explicitly")
}

// Launch starts a headless browser with a fresh profile directory.
func Launch(opts Options) (*Browser, error) {
	path := opts.Path
	if path == "" {
		var err error
		if path, err = Find(); err != nil {
			return nil, err
		}
	}
	sweepProfiles()
	dir, err := os.MkdirTemp("", profilePrefix)
	if err != nil {
		return nil, err
	}
	os.WriteFile(filepath.Join(dir, ownerFile), []byte(strconv.Itoa(os.Getpid())), 0o644)

	args := []string{
		"--headless=new",
		"--remote-debugging-pipe",
		"--user-data-dir=" + dir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-sync",
		// msWindowTabManagerPublic is Edge asking Explorer to list each tab
		// in the Alt-Tab switcher. Explorer keeps the entry after a headless
		// Edge exits, and the user can neither switch to it nor close it.
		"--disable-features=Translate,msEdgeSidebarV2,msWindowTabManagerPublic",
		"--hide-scrollbars",
		"--mute-audio",
		"--force-device-scale-factor=1",
	}
	if !opts.GPU {
		args = append(args, "--disable-gpu")
	}
	args = append(args, opts.Args...)

	// toBrowser carries our commands, fromBrowser the answers and events.
	toBrowserR, toBrowserW, err := os.Pipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	fromBrowserR, fromBrowserW, err := os.Pipe()
	if err != nil {
		toBrowserR.Close()
		toBrowserW.Close()
		os.RemoveAll(dir)
		return nil, err
	}

	cmd := exec.Command(path, args...)
	err = startWithPipes(cmd, toBrowserR, fromBrowserW)
	toBrowserR.Close()
	fromBrowserW.Close()
	if err != nil {
		toBrowserW.Close()
		fromBrowserR.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start %s: %w", path, err)
	}

	b := &Browser{Conn: NewConn(fromBrowserR, toBrowserW), Path: path, cmd: cmd, dir: dir}
	var v struct {
		Product string `json:"product"`
	}
	if err := b.Conn.Call("", "Browser.getVersion", nil, &v); err != nil {
		b.Close()
		return nil, fmt.Errorf("browser did not answer on the debugging pipe: %w", err)
	}
	return b, nil
}

const (
	profilePrefix = "slate-profile-"
	ownerFile     = "slate-owner.pid" // holds the process ID of the program that made the profile
)

// sweepProfiles deletes the profile directories of programs that exited
// without closing their browser. A browser whose owner is gone has exited
// too: it stops when its debugging pipe closes.
func sweepProfiles() {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), profilePrefix+"*"))
	for _, dir := range dirs {
		data, err := os.ReadFile(filepath.Join(dir, ownerFile))
		if err != nil {
			continue // not ours to judge
		}
		if pid, err := strconv.Atoi(string(data)); err == nil && !alive(pid) {
			os.RemoveAll(dir)
		}
	}
}

// Version returns the browser's product string, for example "Edg/157.0.0.0".
func (b *Browser) Version() string {
	var v struct {
		Product string `json:"product"`
	}
	b.Conn.Call("", "Browser.getVersion", nil, &v)
	return v.Product
}

// NewPage opens a blank page and returns the session ID to address it with.
func (b *Browser) NewPage() (string, error) {
	_, session, err := b.OpenPage()
	return session, err
}

// OpenPage opens a blank page. It returns the page's target ID, which
// ClosePage takes, and the session ID to address the page with.
func (b *Browser) OpenPage() (target, session string, err error) {
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := b.Conn.Call("", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
		return "", "", err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	err = b.Conn.Call("", "Target.attachToTarget",
		map[string]any{"targetId": created.TargetID, "flatten": true}, &attached)
	return created.TargetID, attached.SessionID, err
}

// ClosePage closes a page that OpenPage opened and ends its subscriptions.
func (b *Browser) ClosePage(target, session string) error {
	b.Conn.Forget(session)
	return b.Conn.Call("", "Target.closeTarget", map[string]any{"targetId": target}, nil)
}

// Close stops the browser and deletes its profile directory.
func (b *Browser) Close() error {
	b.Conn.Send("", "Browser.close", nil)
	exited := make(chan struct{})
	go func() {
		b.cmd.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		b.cmd.Process.Kill()
		<-exited
	}
	b.Conn.Close()
	// The browser's child processes can hold files for a moment after it exits.
	var err error
	for i := 0; i < 20; i++ {
		if err = os.RemoveAll(b.dir); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}
