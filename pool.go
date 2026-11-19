package slate

import (
	"sync"

	"github.com/lawsmd/slate/browser"
)

// pool holds the one browser that every editor of this process shares. Each
// editor is a page in it.
var pool struct {
	mu    sync.Mutex
	br    *browser.Browser
	users int  // editors, plus one while held
	warm  bool // Warm was called and Shutdown was not
	held  bool // Warm's own use of the browser is counted in users
}

// launch starts the pool's browser. The caller holds pool.mu.
func launch(path string) error {
	br, err := browser.Launch(browser.Options{Path: path})
	if err != nil {
		return err
	}
	pool.br = br
	return nil
}

// acquire returns the shared browser for one more user and starts it when
// none runs.
func acquire(path string) (*browser.Browser, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.br != nil {
		select {
		case <-pool.br.Conn.Done(): // it died while nobody watched
			go pool.br.Close()
			pool.br = nil
		default:
		}
	}
	if pool.br == nil {
		if err := launch(path); err != nil {
			return nil, err
		}
	}
	pool.users++
	return pool.br, nil
}

// replace returns the browser to use in place of old, which a user found
// dead or with a crashed page. The first user to ask starts a new browser;
// the others get that one. A browser that still runs is returned as it is.
func replace(old *browser.Browser, path string) (*browser.Browser, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.br == old && old != nil {
		select {
		case <-old.Conn.Done():
			go old.Close() // collects the process and deletes its profile
			pool.br = nil
		default:
		}
	}
	if pool.br == nil {
		if err := launch(path); err != nil {
			return nil, err
		}
	}
	return pool.br, nil
}

// release ends one use of the shared browser and stops it after the last.
func release() error {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.users--; pool.users > 0 || pool.br == nil {
		return nil
	}
	br := pool.br
	pool.br = nil
	return br.Close()
}

// Warm starts the shared browser in the background and keeps it running
// until Shutdown, so that New has no browser to wait for. A host calls it at
// startup when it will open an editor later. An error shows at the next New,
// which then starts the browser itself.
func Warm() {
	pool.mu.Lock()
	already := pool.warm
	pool.warm = true
	pool.mu.Unlock()
	if already {
		return
	}
	go func() {
		_, err := acquire("")
		pool.mu.Lock()
		stillWanted := pool.warm
		pool.held = err == nil && stillWanted
		if err != nil {
			pool.warm = false
		}
		pool.mu.Unlock()
		if err == nil && !stillWanted {
			release() // Shutdown came first
		}
	}()
}

// Shutdown ends what Warm started. The browser stops when the last editor
// has closed.
func Shutdown() error {
	pool.mu.Lock()
	held := pool.held
	pool.warm, pool.held = false, false
	pool.mu.Unlock()
	if !held {
		return nil
	}
	return release()
}
