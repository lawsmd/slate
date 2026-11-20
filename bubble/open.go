package bubble

import (
	"errors"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lawsmd/slate"
	"github.com/lawsmd/slate/herdr"
)

// Config describes the block that Open makes.
type Config struct {
	// Options are the editor's: the document, its target, the mention
	// search. Fields set in Options.Theme win over the terminal's colours
	// and font.
	slate.Options

	// Title and Meta tell programs that reach the editor through package
	// link what the document is, as Describe does.
	Title string
	Meta  map[string]string

	// Private keeps the document out of package link: no other program can
	// read or edit it.
	Private bool
}

// Open makes an editor and its block, ready for the terminal the host runs
// in. It is what a host needs in place of slate.New, New and Through:
//
//   - In a Herdr pane it draws around Herdr (see package herdr).
//   - It takes colours and the code font from Windows Terminal's settings
//     where they can be read, follows a change of colour scheme in a Herdr
//     pane, and uses the colours the terminal itself reports.
//   - It refuses to start under tmux or Zellij, which drop the image, unless
//     SLATE_FORCE=1 is set.
//
// The model owns what Open made: Close closes the editor as well. Editor
// returns the editor, for reading and exporting the document.
func Open(cfg Config) (*Model, error) {
	if os.Getenv("SLATE_FORCE") == "" {
		for _, m := range []struct{ env, name string }{{"TMUX", "tmux"}, {"ZELLIJ", "Zellij"}} {
			if os.Getenv(m.env) != "" {
				return nil, errors.New("this shell runs inside " + m.name + ", which does not pass Sixel images " +
					"to the terminal, so the editor would show as an empty box. " +
					"Run in a plain terminal tab, or set SLATE_FORCE=1 to start anyway")
			}
		}
	}
	var pane *herdr.Pane
	if herdr.Inside() {
		var err error
		if pane, err = herdr.Open(); err != nil {
			return nil, fmt.Errorf("this shell runs inside Herdr, and slate could not reach the terminal behind it: %w", err)
		}
	}
	// The terminal has no escape sequence that reports its font, and a Herdr
	// pane answers no colour query, so both start from the settings file.
	// Outside Herdr the terminal reports its colours itself.
	terminal, _ := slate.WindowsTerminalTheme()
	if pane == nil {
		terminal.Background, terminal.Foreground, terminal.Selection = "", "", ""
	}
	opts := cfg.Options
	custom := opts.Theme
	opts.Theme = terminal
	ed, err := slate.New(opts)
	if err != nil {
		if pane != nil {
			pane.Close()
		}
		return nil, err
	}
	ed.SetTheme(custom)

	m := newModel(ed, !cfg.Private)
	m.owned, m.pane, m.terminal, m.custom = true, pane, terminal, custom
	if pane != nil {
		m.paint = Herdr(pane)
	}
	if cfg.Title != "" || cfg.Meta != nil {
		m.Describe(cfg.Title, cfg.Meta)
	}
	return m, nil
}

// Editor returns the editor the block shows.
func (m *Model) Editor() *slate.Editor { return m.ed }

// themeMsg is the theme read again from Windows Terminal's settings.
type themeMsg slate.Theme

// themeEvery is how often a block in a Herdr pane reads the settings again.
const themeEvery = 2 * time.Second

func watchTheme() tea.Cmd {
	return tea.Tick(themeEvery, func(time.Time) tea.Msg {
		t, _ := slate.WindowsTerminalTheme()
		return themeMsg(t)
	})
}

// setTheme applies colours from the terminal. The host's own stay on top.
func (m *Model) setTheme(t slate.Theme) {
	m.ed.SetTheme(t)
	m.ed.SetTheme(m.custom)
}

// terminalTheme handles what the terminal says about its colours, for a
// model that Open made. It reports whether msg was one of those messages.
func (m *Model) terminalTheme(msg tea.Msg) (tea.Cmd, bool) {
	if !m.owned {
		return nil, false
	}
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.setTheme(slate.Theme{Background: msg.String()})
	case tea.ForegroundColorMsg:
		m.setTheme(slate.Theme{Foreground: msg.String()})
	case themeMsg:
		if t, old := slate.Theme(msg), m.terminal; t.Background != old.Background || t.Foreground != old.Foreground ||
			t.Selection != old.Selection || t.MonoFont != old.MonoFont {
			m.terminal = t
			m.setTheme(t)
		}
		return watchTheme(), true
	default:
		return nil, false
	}
	return nil, true
}
