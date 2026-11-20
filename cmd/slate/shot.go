package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lawsmd/slate"
)

// shot renders a Markdown file and writes the editor's frame as a PNG. It
// needs no terminal, so it serves for checking the rendering.
func shot(args []string) error {
	fs := flag.NewFlagSet("shot", flag.ExitOnError)
	out := fs.String("o", "slate.png", "PNG file to write")
	cols := fs.Int("cols", 80, "width in cells")
	rows := fs.Int("rows", 40, "height in cells")
	keys := fs.String("keys", "", "keys to press after loading, separated by spaces: a character, a key name such as Enter or ArrowDown, or ctrl+b; \"space\" for a space, \"wait\" to pause")
	blur := fs.Bool("blur", false, "take the focus away before the picture")
	adf := fs.Bool("adf", false, "print the document as ADF")
	md := fs.Bool("md", false, "print the document as Markdown")
	fs.Parse(args)

	var doc []byte
	if fs.NArg() > 0 {
		var err error
		if doc, err = os.ReadFile(fs.Arg(0)); err != nil {
			return err
		}
	}
	start := time.Now()
	ed, err := slate.New(slate.Options{Markdown: string(doc), Cols: *cols, Rows: *rows, Mentions: demoMentions,
		SteadyCursor: true, // the picture does not depend on when it is taken
	})
	if err != nil {
		return err
	}
	defer ed.Close()
	ctx := context.Background()
	fmt.Fprintln(os.Stderr, "editor ready in", time.Since(start).Round(time.Millisecond))

	for _, word := range strings.Fields(*keys) {
		if word == "wait" {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		var k slate.Key
		parts := strings.Split(word, "+")
		for _, mod := range parts[:len(parts)-1] {
			switch mod {
			case "ctrl":
				k.Mod |= slate.Ctrl
			case "alt":
				k.Mod |= slate.Alt
			case "shift":
				k.Mod |= slate.Shift
			}
		}
		k.Name = parts[len(parts)-1]
		if word == "+" {
			k.Name = "+"
		}
		if k.Name == "space" {
			k.Name = " "
		}
		ed.Key(k)
		time.Sleep(15 * time.Millisecond)
	}
	if *blur {
		ed.Focus(false)
	}
	time.Sleep(400 * time.Millisecond)

	if *md {
		s, err := ed.Markdown(ctx)
		if err != nil {
			return err
		}
		fmt.Println(s)
	}
	if *adf {
		doc, err := ed.ADF(ctx)
		if err != nil {
			return err
		}
		fmt.Println(string(doc))
	}
	png, err := ed.Screenshot(ctx)
	if err != nil {
		return err
	}
	return os.WriteFile(*out, png, 0o644)
}
