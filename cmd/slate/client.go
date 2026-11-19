package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

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
	asJSON := fs.Bool("json", false, "print the Markdown with its revision and the selected text, as JSON")
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
	old := fs.String("old", "", "the text to replace; it must occur exactly once. Empty adds -new at the end")
	replacement := fs.String("new", "", "the text to put in its place")
	by := fs.String("by", "agent", "who is writing, as the host is told")
	fs.Parse(args)
	if *old == "" && *replacement == "" {
		return errors.New("edit needs -old, -new or both, before the editor's ID")
	}
	rev, err := link.Edit(fs.Arg(0), *old, *replacement, *by)
	if err != nil {
		return err
	}
	fmt.Println("revision", rev)
	return nil
}

func watch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	fs.Parse(args)
	return link.Watch(fs.Arg(0), func(c link.Change) bool {
		fmt.Printf("revision %d by %s\n", c.Rev, c.Origin)
		return true
	})
}
