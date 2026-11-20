package bubble

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lawsmd/slate"
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

	// Painter puts the image on the screen. Leave it nil and Open chooses
	// one for the terminal or multiplexer the program runs in. The caller
	// closes a painter it sets here.
	Painter Painter
}

// Open makes an editor and its block, ready for the terminal the host runs
// in. It is what a host needs in place of slate.New, New and Through:
//
//   - It finds the terminal or multiplexer the program runs in among the
//     hosts slate knows and those added with Register, and takes that host's
//     painter. In a Herdr pane it draws around Herdr (see package herdr).
//     Config.Painter names the painter and skips the search.
//   - It takes colours and the code font from Windows Terminal's settings
//     where they can be read, follows a change of colour scheme in a host
//     that answers no colour query, and otherwise uses the colours the
//     terminal itself reports.
//   - It refuses to start under tmux or Zellij, which drop the image, unless
//     SLATE_FORCE=1 is set.
//
// The model owns what Open made: Close closes the editor as well. Editor
// returns the editor, for reading and exporting the document.
func Open(cfg Config) (*Model, error) {
	paint, host := cfg.Painter, Host{}
	if paint == nil {
		var err error
		if paint, host, err = findPainter(); err != nil {
			return nil, err
		}
	}
	closeHost := func() {}
	if c, ok := paint.(interface{ Close() }); ok && cfg.Painter == nil {
		closeHost = c.Close
	}
	// The terminal has no escape sequence that reports its font, and a mute
	// host answers no colour query, so both start from the settings file.
	// Elsewhere the terminal reports its colours itself.
	terminal, _ := slate.WindowsTerminalTheme()
	if !host.Mute {
		terminal.Background, terminal.Foreground, terminal.Selection = "", "", ""
	}
	opts := cfg.Options
	custom := opts.Theme
	opts.Theme = terminal
	ed, err := slate.New(opts)
	if err != nil {
		closeHost()
		return nil, err
	}
	ed.SetTheme(custom)

	m := newModel(ed, !cfg.Private)
	m.owned, m.closeHost, m.mute, m.terminal, m.custom = true, closeHost, host.Mute, terminal, custom
	m.paint = paint
	if cfg.Title != "" || cfg.Meta != nil {
		m.Describe(cfg.Title, cfg.Meta)
	}
	return m, nil
}

// Editor returns the editor the block shows.
func (m *Model) Editor() *slate.Editor { return m.ed }

// themeMsg is the theme read again from Windows Terminal's settings.
type themeMsg slate.Theme

// themeEvery is how often a block in a mute host reads the settings again.
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
