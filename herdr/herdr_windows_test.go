package herdr

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A pipe with one instance refuses a second client while the first is
// connected. dial has to wait for the server to take the next one.
func TestDialWaitsForBusyPipe(t *testing.T) {
	socket := fmt.Sprintf("slate-test-%d", os.Getpid())
	name, err := windows.UTF16PtrFromString(`\\.\pipe\` + socket)
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX, windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, 1, 512, 512, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(pipe)

	first, err := dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := os.OpenFile(`\\.\pipe\`+socket, os.O_RDWR, 0); !errors.Is(err, windows.ERROR_PIPE_BUSY) {
		if err == nil {
			f.Close()
		}
		t.Fatalf("a second client got %v; want the pipe busy", err)
	}

	// The server lets the first client go and listens again after a while.
	listening := make(chan error, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		first.Close()
		if err := windows.DisconnectNamedPipe(pipe); err != nil {
			listening <- err
			return
		}
		err := windows.ConnectNamedPipe(pipe, nil)
		if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			err = nil
		}
		listening <- err
	}()

	second, err := dial(socket)
	if err != nil {
		t.Fatalf("dial gave up on a busy pipe: %v", err)
	}
	defer second.Close()
	if err := <-listening; err != nil {
		t.Fatal(err)
	}
}

// A pipe that does not exist is an error at once.
func TestDialMissingPipe(t *testing.T) {
	start := time.Now()
	if conn, err := dial(fmt.Sprintf("slate-test-none-%d", os.Getpid())); err == nil {
		conn.Close()
		t.Fatal("dial opened a pipe that does not exist")
	}
	if took := time.Since(start); took > dialWait/2 {
		t.Errorf("dial took %v to refuse a pipe that does not exist", took)
	}
}
