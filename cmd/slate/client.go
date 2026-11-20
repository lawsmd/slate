package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/lawsmd/slate"
	"github.com/lawsmd/slate/link"
)

// printJSON writes v to standard output, indented.
func printJSON(v any) error {
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	return out.Encode(v)
}

func list(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the list as JSON")
	fs.Parse(args)
	all, err := link.List()
	if err != nil {
		return err
	}
	if *asJSON {
		if all == nil {
			all = []link.Entry{}
		}
		return printJSON(all)
	}
	if len(all) == 0 {
		fmt.Println("no slate editor is open")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTARGET\tHOST\tPANE\tTITLE\tMETA")
	for _, e := range all {
		var meta []string
		for k, v := range e.Meta {
			meta = append(meta, k+"="+v)
		}
		sort.Strings(meta)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.Target, e.Exe, e.Pane, e.Title, strings.Join(meta, " "))
	}
	return w.Flush()
}

func read(args []string) error {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the Markdown with its revision, the selected text and each writer's text, as JSON")
	fs.Parse(args)
	doc, err := link.Read(fs.Arg(0))
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(doc)
	}
	_, err = fmt.Print(doc.Markdown)
	return err
}

func edit(args []string) error {
	fs := flag.NewFlagSet("edit", flag.ExitOnError)
	old := fs.String("old", "", "the text to replace; it must occur exactly once. Empty adds the new text at the end")
	oldFile := fs.String("old-file", "", "read the text to replace from this file; - reads standard input")
	replacement := fs.String("new", "", "the text to put in its place")
	newFile := fs.String("new-file", "", "read the text to put in its place from this file; - reads standard input")
	own := fs.Bool("own", false, "replace the text the writer named by -by last put in, as it stands now, instead of -old")
	ifRev := fs.Uint64("if-rev", 0, "refuse the edit unless the document is at this revision, as read -json reports it")
	by := fs.String("by", "agent", "who is writing, as the host is told")
	fs.Parse(args)
	var err error
	if *old, err = textFlag("old", *old, *oldFile); err != nil {
		return err
	}
	if *replacement, err = textFlag("new", *replacement, *newFile); err != nil {
		return err
	}
	if *own && *old != "" {
		return errors.New("edit takes -own or -old, not both")
	}
	if *old == "" && *replacement == "" && !*own {
		return errors.New("edit needs -old, -new, -own or a -file form of them, before the editor's ID")
	}
	rev, err := link.Edit(fs.Arg(0), slate.Edit{Old: *old, New: *replacement, Own: *own, IfRev: *ifRev}, *by)
	if err != nil {
		return err
	}
	fmt.Println("revision", rev)
	return nil
}

// textFlag returns the text of a flag given either inline or as a file,
// where the file - is standard input.
func textFlag(name, inline, file string) (string, error) {
	if file == "" {
		return inline, nil
	}
	if inline != "" {
		return "", fmt.Errorf("edit takes -%s or -%s-file, not both", name, name)
	}
	var data []byte
	var err error
	if file == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(file)
	}
	return string(data), err
}

func watch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	fs.Parse(args)
	return link.Watch(fs.Arg(0), func(c link.Change) bool {
		fmt.Printf("revision %d by %s\n", c.Rev, c.Origin)
		return true
	})
}
