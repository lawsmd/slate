// Command slate is the demo host for the slate editing block.
package main

import (
	"fmt"
	"os"
)

const usage = `usage:
  slate notes file.md          edit a Markdown note; ctrl+s saves
  slate comment [-o out.json]  write a comment; ctrl+s prints it as ADF
  slate shot [flags] file.md   render a file to a PNG, without a terminal`

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
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "slate:", err)
		os.Exit(1)
	}
}
