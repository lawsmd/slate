//go:build !windows

package browser

import (
	"os"
	"os/exec"
	"syscall"
)

// startWithPipes starts cmd with the pipe ends as descriptors 3 and 4, where
// Chromium expects them.
func startWithPipes(cmd *exec.Cmd, childRead, childWrite *os.File) error {
	cmd.ExtraFiles = []*os.File{childRead, childWrite}
	return cmd.Start()
}

// alive reports whether a process with this ID is running.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
