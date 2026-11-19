package browser

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// startWithPipes starts cmd with the two pipe ends inherited. Windows has no
// numbered descriptors to inherit, so Chromium takes the handle values from
// --remote-debugging-io-pipes: the handle it reads from, then the one it
// writes to.
func startWithPipes(cmd *exec.Cmd, childRead, childWrite *os.File) error {
	handles := []syscall.Handle{syscall.Handle(childRead.Fd()), syscall.Handle(childWrite.Fd())}
	for _, h := range handles {
		if err := syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
			return err
		}
	}
	cmd.Args = append(cmd.Args, fmt.Sprintf("--remote-debugging-io-pipes=%d,%d", handles[0], handles[1]))
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: handles}
	return cmd.Start()
}
