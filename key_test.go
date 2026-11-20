package slate

import (
	"context"
	"testing"
)

func TestCtrlBackspaceDeletesAWord(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	ed, err := New(Options{Markdown: "one two three", Cols: 40, Rows: 10})
	if err != nil {
		t.Skip(err)
	}
	defer ed.Close()
	ed.Key(Key{Name: "End", Mod: Ctrl})
	ed.Key(Key{Name: "Backspace", Mod: Ctrl})
	md, err := ed.Markdown(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if md != "one two " {
		t.Fatalf("got %q", md)
	}
}
