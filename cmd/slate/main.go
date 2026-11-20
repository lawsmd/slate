// Command slate is the demo host for the slate editing block, and the
// command-line client for the editors open on this machine.
package main

import (
	"fmt"
	"os"
)

const usage = `usage:
  slate notes file.md          edit a Markdown note; ctrl+s saves
  slate comment [-o out.json]  write a comment; ctrl+s prints it as ADF
  slate shot [flags] file.md   render a file to a PNG, without a terminal

for the editors open in any program on this machine:
  slate ls [-json]             list them
  slate read [-json] [ID]      print an editor's Markdown; -json adds the
                               revision and the text each writer last put in
  slate edit -old TEXT -new TEXT [-by NAME] [-if-rev N] [ID]
                               replace TEXT, which must occur exactly once;
                               without -old, add the new text at the end
  slate edit -own -new TEXT -by NAME [-if-rev N] [ID]
                               replace the text NAME's last edit put in, with
                               any changes made inside it since
                               -old-file and -new-file take the text from a
                               file, or from standard input for -; -if-rev
                               refuses the edit if the document has changed
  slate watch [ID]             print a line for each change until the editor closes

ID may be left out while one editor is open. An edit shows in the editor at
once, highlighted, and one undo removes it. No command saves or sends.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "notes":
		err = notes(os.Args[2:])
	case "comment":
		err = comment(os.Args[2:])
	case "shot":
		err = shot(os.Args[2:])
	case "ls":
		err = list(os.Args[2:])
	case "read":
		err = read(os.Args[2:])
	case "edit":
		err = edit(os.Args[2:])
	case "watch":
		err = watch(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "slate:", err)
		os.Exit(1)
	}
}
