package bubble

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/lawsmd/slate/herdr"
)

// Host is a terminal or multiplexer that Open treats in its own way: it gets
// its own painter, or it is refused because the image cannot reach the screen.
// A terminal that shows what the program writes needs no Host.
type Host struct {
	// Name is used in the error when Open fails.
	Name string
	// Detect reports whether the program runs inside this host.
	Detect func() bool
	// Open returns the painter for this host. A painter with a Close method
	// is closed by the model's Close. An error that wraps ErrNoImage means
	// the host drops the image and there is no way round it.
	Open func() (Painter, error)
	// Mute says the host answers no colour query. The colours then come from
	// Windows Terminal's settings file, which the model reads again every
	// two seconds.
	Mute bool
}

// ErrNoImage is the error of a host that passes no image to the terminal.
// Open starts anyway, with the default painter, when SLATE_FORCE=1 is set.
var ErrNoImage = errors.New("the host does not pass Sixel images to the terminal")

var (
	hostsMu sync.Mutex
	hosts   = []Host{
		refused("tmux", "TMUX"),
		refused("Zellij", "ZELLIJ"),
		{
			Name:   "Herdr",
			Detect: herdr.Inside,
			Open: func() (Painter, error) {
				pane, err := herdr.Open()
				if err != nil {
					return nil, fmt.Errorf("slate could not reach the terminal behind it: %w", err)
				}
				return ownedHerdr{herdrPainter{pane}}, nil
			},
			Mute: true,
		},
	}
)

// Register adds a host that Open looks for before the ones slate knows. A
// program calls it at start, before the first Open, to draw in a multiplexer
// slate has no painter for.
func Register(h Host) {
	hostsMu.Lock()
	defer hostsMu.Unlock()
	hosts = append([]Host{h}, hosts...)
}

// refused is a host that drops the image, found by an environment variable
// it sets in its panes.
func refused(name, env string) Host {
	return Host{
		Name:   name,
		Detect: func() bool { return os.Getenv(env) != "" },
		Open: func() (Painter, error) {
			return nil, fmt.Errorf("%w, so the editor would show as an empty box. "+
				"Run in a plain terminal tab, or set SLATE_FORCE=1 to start anyway", ErrNoImage)
		},
	}
}

// findPainter returns the painter of the first host the program runs in, and
// that host. With no such host it returns the default painter and a Host
// with no name.
func findPainter() (Painter, Host, error) {
	hostsMu.Lock()
	list := append([]Host(nil), hosts...)
	hostsMu.Unlock()
	force := os.Getenv("SLATE_FORCE") != ""
	for _, h := range list {
		if !h.Detect() {
			continue
		}
		p, err := h.Open()
		if errors.Is(err, ErrNoImage) && force {
			continue
		}
		if err != nil {
			return nil, h, fmt.Errorf("this shell runs inside %s: %w", h.Name, err)
		}
		return p, h, nil
	}
	return stdout{}, Host{}, nil
}

// ownedHerdr is the Herdr painter that Open makes. The model closes its pane.
type ownedHerdr struct{ herdrPainter }

func (o ownedHerdr) Close() { o.pane.Close() }
