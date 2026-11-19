package bubble

import (
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
