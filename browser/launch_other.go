//go:build !windows

package browser

import (
	"os"
	"os/exec"
)

// startWithPipes starts cmd with the pipe ends as descriptors 3 and 4, where
// Chromium expects them.
func startWithPipes(cmd *exec.Cmd, childRead, childWrite *os.File) error {
	cmd.ExtraFiles = []*os.File{childRead, childWrite}
	return cmd.Start()
}
