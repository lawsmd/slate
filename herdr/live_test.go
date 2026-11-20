package herdr

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests talk to the Herdr the test runs in, and are skipped anywhere
// else. Run them after updating Herdr: they fail when Herdr no longer has a
// call, an event or a field that this package uses.

func TestLiveSubscription(t *testing.T) {
	if !Inside() {
		t.Skip("not in a Herdr pane")
	}
	p := &Pane{id: os.Getenv("HERDR_PANE_ID"), socket: os.Getenv("HERDR_SOCKET_PATH")}
	conn, err := dial(p.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write(subscription()); err != nil {
		t.Fatal(err)
	}
	// Herdr answers a subscription it refuses with an error line. One it
	// accepts may stay silent until an event.
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(conn).ReadString('\n')
		line <- s
	}()
	select {
	case s := <-line:
		if strings.Contains(s, `"error"`) {
			t.Fatalf("Herdr refused the subscription: %s", s)
		}
	case <-time.After(2 * time.Second):
	}
}

func TestLivePane(t *testing.T) {
	if !Inside() {
		t.Skip("not in a Herdr pane")
	}
	p, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	st := p.State()
	p.mu.Lock()
	cols, rows := p.cols, p.rows
	p.mu.Unlock()
	t.Logf("pane at %d,%d, %d by %d cells, in a terminal of %d by %d; visible %v", st.X, st.Y, st.W, st.H, cols, rows, st.Visible)
	if st.W < 1 || st.H < 1 {
		t.Fatal("the snapshot does not hold this pane")
	}
	if st.X+st.W > cols || st.Y+st.H > rows {
		t.Fatal("the pane does not fit in the terminal")
	}
}
