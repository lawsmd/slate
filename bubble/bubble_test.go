package bubble

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lawsmd/slate"
)

func TestTranslate(t *testing.T) {
	for _, c := range []struct {
		name string
		in   tea.KeyPressMsg
		key  slate.Key
		text string
		ok   bool
	}{
		{"a letter", tea.KeyPressMsg{Code: 'a', Text: "a"}, slate.Key{Name: "a", Text: "a"}, "", true},
		{"a capital keeps no shift", tea.KeyPressMsg{Code: 'a', Text: "A", Mod: tea.ModShift},
			slate.Key{Name: "A", Text: "A"}, "", true},
		{"a named key", tea.KeyPressMsg{Code: tea.KeyEnter}, slate.Key{Name: "Enter"}, "", true},
		{"a named key with shift", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift},
			slate.Key{Name: "ArrowLeft", Mod: slate.Shift}, "", true},
		{"space", tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, slate.Key{Name: " "}, "", true},
		{"a shortcut", tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}, slate.Key{Name: "b", Mod: slate.Ctrl}, "", true},
		{"ctrl+backspace as the terminal sends it", tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl},
			slate.Key{Name: "Backspace", Mod: slate.Ctrl}, "", true},
		{"the shell's delete-word key", tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl},
			slate.Key{Name: "Backspace", Mod: slate.Ctrl}, "", true},
		{"composed input", tea.KeyPressMsg{Text: "日本"}, slate.Key{}, "日本", false},
		{"a key with no use", tea.KeyPressMsg{Code: tea.KeyF13}, slate.Key{}, "", false},
	} {
		key, text, ok := translate(c.in)
		if key != c.key || text != c.text || ok != c.ok {
			t.Errorf("%s: got %+v, %q, %v; want %+v, %q, %v", c.name, key, text, ok, c.key, c.text, c.ok)
		}
	}
}

func TestStdoutPainterKeepsTheHostsCells(t *testing.T) {
	at, ok := stdout{}.Place(100, 40, Rect{X: 1, Y: 2, W: 30, H: 10})
	if !ok || at != (Rect{X: 1, Y: 2, W: 30, H: 10}) {
		t.Fatalf("got %+v, %v", at, ok)
	}
}

// plainShell clears what the hosts slate knows look for, and puts the list
// of hosts back after the test.
func plainShell(t *testing.T) {
	for _, env := range []string{"TMUX", "ZELLIJ", "HERDR_ENV", "HERDR_PANE_ID", "SLATE_FORCE"} {
		t.Setenv(env, "")
	}
	saved := append([]Host(nil), hosts...)
	t.Cleanup(func() { hosts = saved })
}

func TestFindPainterInAPlainTerminal(t *testing.T) {
	plainShell(t)
	p, h, err := findPainter()
	if _, ok := p.(stdout); !ok || h.Name != "" || err != nil {
		t.Fatalf("got %T, %q, %v", p, h.Name, err)
	}
}

func TestFindPainterRefusesAHostThatDropsTheImage(t *testing.T) {
	for env, name := range map[string]string{"TMUX": "tmux", "ZELLIJ": "Zellij"} {
		plainShell(t)
		t.Setenv(env, "1")
		_, h, err := findPainter()
		if !errors.Is(err, ErrNoImage) || h.Name != name || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: got %q, %v", env, h.Name, err)
		}
		t.Setenv("SLATE_FORCE", "1")
		p, _, err := findPainter()
		if _, ok := p.(stdout); !ok || err != nil {
			t.Errorf("%s with SLATE_FORCE: got %T, %v", env, p, err)
		}
		t.Setenv(env, "")
	}
}

func TestFindPainterKnowsHerdr(t *testing.T) {
	plainShell(t)
	for _, h := range hosts {
		if h.Name != "Herdr" {
			continue
		}
		if h.Detect() || !h.Mute {
			t.Fatalf("outside Herdr: detected %v, mute %v", h.Detect(), h.Mute)
		}
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_PANE_ID", "w1:p1")
		if !h.Detect() {
			t.Fatal("not detected in a Herdr pane")
		}
		return
	}
	t.Fatal("no host named Herdr")
}

// fakePainter stands for the painter of a multiplexer slate does not know.
type fakePainter struct{ stdout }

func TestRegisteredHostComesFirst(t *testing.T) {
	plainShell(t)
	t.Setenv("TMUX", "1")
	Register(Host{
		Name:   "fake",
		Detect: func() bool { return true },
		Open:   func() (Painter, error) { return fakePainter{}, nil },
		Mute:   true,
	})
	p, h, err := findPainter()
	if _, ok := p.(fakePainter); !ok || h.Name != "fake" || !h.Mute || err != nil {
		t.Fatalf("got %T, %+v, %v", p, h.Name, err)
	}
}

func TestAHostsOwnErrorIsNotForced(t *testing.T) {
	plainShell(t)
	t.Setenv("SLATE_FORCE", "1")
	Register(Host{
		Name:   "fake",
		Detect: func() bool { return true },
		Open:   func() (Painter, error) { return nil, errors.New("no pipe") },
	})
	if _, _, err := findPainter(); err == nil || !strings.Contains(err.Error(), "inside fake: no pipe") {
		t.Fatalf("got %v", err)
	}
}
