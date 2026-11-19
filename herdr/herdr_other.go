//go:build !windows

package herdr

import (
	"errors"
	"io"
	"net"
)

var errUnsupported = errors.New("drawing from a Herdr pane works on Windows only")

func dial(socket string) (io.ReadWriteCloser, error) { return net.Dial("unix", socket) }

func startHelper(string) (io.WriteCloser, io.Reader, func(), error) {
	return nil, nil, nil, errUnsupported
}
