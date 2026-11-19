package slate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lawsmd/slate/browser"
)

// solid returns a frame of the given size in one colour.
func solid(w, h int, c byte) frame {
	return frame{pix: bytes.Repeat([]byte{c, c, c, 255}, w*h), stride: w * 4, w: w, h: h}
}

func TestDrawSendsOnlyChangedRows(t *testing.T) {
	e := &Editor{col: 3, row: 5, cols: 4, rows: 3, cellW: 2, cellH: 6}
	if e.Draw() != nil {
		t.Fatal("Draw before the first frame should return nil")
	}

	e.frame = solid(8, 18, 10)
	first := string(e.Draw())
	if !strings.HasPrefix(first, "\x1b7\x1b[5;3H\x1bP") || !strings.HasSuffix(first, "\x1b\\\x1b8") {
		t.Fatalf("first draw should be one image at the block's corner inside a cursor save and restore: %q", first)
	}
	if strings.Count(first, "\x1bP") != 1 {
		t.Fatalf("first draw should be one image: %q", first)
	}
	if e.Draw() != nil {
		t.Fatal("Draw without a new frame should return nil")
	}

	// Change one pixel in the last cell row.
	next := solid(8, 18, 10)
	next.pix[(17*8+7)*4] = 200
	e.frame = next
	second := string(e.Draw())
	if !strings.HasPrefix(second, "\x1b7\x1b[7;3H\x1bP") || strings.Count(second, "\x1bP") != 1 {
		t.Fatalf("a change in the third row should draw at terminal row 7 only: %q", second)
	}
	if !strings.Contains(second, `"1;1;8;6`) {
		t.Fatalf("the image should be one cell row tall: %q", second)
	}

	e.Invalidate()
	if again := string(e.Draw()); !strings.HasPrefix(again, "\x1b7\x1b[5;3H") {
		t.Fatalf("Draw after Invalidate should send the whole block: %q", again)
	}
}

func TestDrawCropsAFrameLargerThanTheBlock(t *testing.T) {
	// A frame from before a resize can be larger than the block is now.
	e := &Editor{col: 1, row: 1, cols: 2, rows: 1, cellW: 2, cellH: 6}
	e.frame = solid(8, 18, 10)
	if out := string(e.Draw()); !strings.Contains(out, `"1;1;4;6`) {
		t.Fatalf("the image should be cropped to 4 by 6 pixels: %q", out)
	}
}

func TestThemeVars(t *testing.T) {
	got := Theme{Background: "#000", FontSize: 14, Vars: map[string]string{"warning": "#ff0"}}.vars()
	want := map[string]string{"bg": "#000", "size": "14px", "warning": "#ff0"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestWindowsTerminalTheme(t *testing.T) {
	settings := []byte(`{
		// comments and trailing commas are allowed
		"defaultProfile": "{a}",
		"profiles": {
			"defaults": {"colorScheme": "Night", "font": {"face": "Fira // Code",},},
			"list": [
				{"guid": "{a}", "name": "one"},
				{"guid": "{b}", "colorScheme": {"dark": "Day", "light": "Night"}, "background": "#010203"},
			],
		},
		"schemes": [
			{"name": "Night", "background": "#000000", "foreground": "#ffffff"},
			{"name": "Day", "background": "#eeeeee", "foreground": "#111111", "selectionBackground": "#cccccc"},
		], /* the end */
	}`)
	got, ok := windowsTerminalTheme(settings, "")
	if !ok || got.Background != "#000000" || got.Foreground != "#ffffff" || !strings.HasPrefix(got.MonoFont, `"Fira // Code"`) {
		t.Fatalf("default profile: %+v, %v", got, ok)
	}
	got, _ = windowsTerminalTheme(settings, "{B}")
	if got.Background != "#010203" || got.Foreground != "#111111" || got.Selection != "#cccccc" {
		t.Fatalf("profile with its own scheme and background: %+v", got)
	}
	if _, ok := windowsTerminalTheme([]byte(`{"profiles": {}}`), ""); ok {
		t.Fatal("settings without colours or font should not be ok")
	}
}

// TestEditor runs the real editor in a headless browser.
func TestEditor(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := browser.Find(); err != nil {
		t.Skip(err)
	}
	ed, err := New(Options{
		Markdown: "# Title\n",
		Cols:     40, Rows: 10,
		Mentions: func(_ context.Context, query string) ([]Mention, error) {
			return []Mention{{ID: "id:1", Name: "Ada " + query}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ed.Close()

	select {
	case <-ed.Updates():
	case <-time.After(5 * time.Second):
		t.Fatal("no frame arrived")
	}
	if out := ed.Draw(); !bytes.HasPrefix(out, []byte("\x1b7\x1b[1;1H\x1bP")) {
		t.Fatalf("first draw: %q", out[:min(len(out), 40)])
	}
	if ed.Modified() {
		t.Fatal("a fresh editor should not be modified")
	}

	// Go to the end and type a paragraph with a mention and a code block.
	keys := []Key{{Name: "End", Mod: Ctrl}, {Name: "Enter"}, {Name: "h"}, {Name: "i"}, {Name: " "}, {Name: "@"}, {Name: "l"}}
	for _, k := range keys {
		if err := ed.Key(k); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(500 * time.Millisecond) // the completion list asks the host
	for _, k := range []Key{{Name: "Enter"}, {Name: "Enter"}, {Name: "`"}, {Name: "`"}, {Name: "`"}, {Name: "x"}} {
		if err := ed.Key(k); err != nil {
			t.Fatal(err)
		}
	}
	if err := ed.Paste(" pasted"); err != nil {
		t.Fatal(err)
	}

	md, err := ed.Markdown()
	if err != nil {
		t.Fatal(err)
	}
	want := "# Title\n\nhi [@Ada l](mention:id%3A1) \n```\nx pasted\n```"
	if md != want {
		t.Fatalf("Markdown:\n%q\nwant:\n%q", md, want)
	}
	if !ed.Modified() {
		t.Fatal("typing should mark the editor modified")
	}

	raw, err := ed.ADF()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, n := range doc.Content {
		types = append(types, n.Type)
	}
	if got := strings.Join(types, " "); doc.Type != "doc" || got != "heading paragraph codeBlock" {
		t.Fatalf("ADF blocks: %s %q", doc.Type, got)
	}
	if !strings.Contains(string(raw), `"type":"mention","attrs":{"id":"id:1","text":"@Ada l"}`) {
		t.Fatalf("ADF has no mention: %s", raw)
	}

	if err := ed.Key(Key{Name: "a", Mod: Ctrl}); err != nil {
		t.Fatal(err)
	}
	if sel, err := ed.Copy(); err != nil || sel != want {
		t.Fatalf("Copy after select-all: %q, %v", sel, err)
	}

	png, err := ed.Screenshot()
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("Screenshot: %v", err)
	}
}
