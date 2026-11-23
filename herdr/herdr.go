// Package herdr draws a slate block from inside a Herdr pane.
//
// Herdr redraws its panes cell by cell and passes no Sixel images to the
// terminal it runs in. This package goes around it. A helper process attaches
// to the console of Herdr's client, which is the real terminal, and writes
// the image there. Herdr's socket API says where the pane sits in its pane
// area and whether it is showing. Herdr counts that place from the top
// left of the pane area, which leaves out the sidebar and the tab bar, and no
// call reports where the area starts. The area fills the bottom right of the
// terminal, so the helper reads the terminal's size and the host adds the
// difference.
//
// Herdr does not know the image exists. It rewrites only the cells whose text
// changes, so parts of an image stay behind in cells that are blank before
// and after a change of view. EraseBlank removes them.
//
// Herdr also repaints every cell at times, as when its window gets the
// keyboard focus back, and that removes the image. Draw marks the block's
// cells with a colour that shows on no blank cell, and the helper watches
// them. When Herdr has written over the marks, Wiped signals and the host
// draws again. The host ends the watch with Unwatch before it moves the
// block, whose old cells Herdr then writes over as it does in a repaint.
package herdr

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// sizeWait is how long Open waits for the helper to report the terminal's size.
const sizeWait = 2 * time.Second

// Inside reports whether this process runs in a Herdr pane.
func Inside() bool { return os.Getenv("HERDR_ENV") != "" && os.Getenv("HERDR_PANE_ID") != "" }

// State is where the pane is and whether it can be drawn on.
type State struct {
	X, Y, W, H int  // the pane's cells in the outer terminal, counted from 0
	Visible    bool // the pane's tab is showing, no zoomed pane covers it, and its place is known
	Focused    bool // the pane has Herdr's focus
}

// Pane is this process's Herdr pane.
type Pane struct {
	id     string
	socket string
	out    io.WriteCloser // the helper's standard input
	wiped  chan struct{}
	stop   func()

	mu         sync.Mutex
	state      State
	cols, rows int // the outer terminal's size in cells, 0 until the helper reports it
	changes    chan struct{}
	done       chan struct{}
}

// Open finds the pane this process runs in and starts the helper that writes
// to the outer terminal.
func Open() (*Pane, error) {
	if !Inside() {
		return nil, errors.New("not in a Herdr pane")
	}
	p := &Pane{
		id:      os.Getenv("HERDR_PANE_ID"),
		socket:  os.Getenv("HERDR_SOCKET_PATH"),
		changes: make(chan struct{}, 1),
		wiped:   make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	if _, err := p.query(); err != nil {
		return nil, err
	}
	out, reports, stop, err := startHelper(p.socket)
	if err != nil {
		return nil, err
	}
	p.out, p.stop = out, stop
	sized := make(chan struct{})
	go p.read(reports, sized)
	select {
	case <-sized:
	case <-time.After(sizeWait):
		p.Close()
		return nil, errors.New("the helper did not report the terminal's size")
	}
	st, err := p.query()
	if err != nil {
		p.Close()
		return nil, err
	}
	p.mu.Lock()
	p.state = st
	p.mu.Unlock()
	go p.watch()
	go p.poll()
	return p, nil
}

// Close stops the helper and the watchers.
func (p *Pane) Close() {
	close(p.done)
	p.out.Close()
	p.stop()
}

// State returns the pane's last known place.
func (p *Pane) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Changes signals that State returns something new.
func (p *Pane) Changes() <-chan struct{} { return p.changes }

// Write sends escape sequences to the outer terminal. Positions in them are
// outer terminal cells.
func (p *Pane) Write(seq []byte) error { return p.send('W', seq) }

// Draw writes a whole image, as Write does, and marks the outer cells it
// covers, counted from 0. From then on Wiped signals when Herdr has written
// over those cells and they are blank again.
func (p *Pane) Draw(x, y, w, h int, seq []byte) error {
	return p.send('F', append(rectBytes(x, y, w, h), seq...))
}

// Refresh writes a whole image over the one that Draw placed and keeps the
// marks as they are. The helper remembers this image and writes it again
// after a wipe, so call it when the block has changed since the last Draw.
func (p *Pane) Refresh(seq []byte) error { return p.send('K', seq) }

// Unwatch ends the watch that Draw started and makes the helper forget the
// image it kept. Call it when the host moves or covers the block: the cells
// the block leaves turn blank, which the helper would take for a wipe, and
// it would write the image again in the place the block has left.
func (p *Pane) Unwatch() error { return p.send('U', nil) }

// CellSize returns the pixel size of one cell of the outer terminal, for a
// Sixel image. The helper writes to a Windows console, and Windows Terminal,
// the terminal there that shows Sixel, draws an image on cells of 10 by 20
// pixels at every font size. Herdr has no call that reports a cell's size,
// and what a pane answers to CSI 16 t is not the size Sixel is drawn at.
func (p *Pane) CellSize() (w, h int, ok bool) { return 10, 20, true }

// Wiped signals that Herdr repainted the cells given to Draw and the image
// is gone from them. The host answers with another Draw.
func (p *Pane) Wiped() <-chan struct{} { return p.wiped }

func rectBytes(v ...int) []byte {
	b := make([]byte, 0, 16)
	for _, v := range v {
		b = append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	}
	return b
}

// EraseBlank removes image remains from the given outer cells, counted from
// 0. It writes a space over each cell that already holds a plain or marked
// space and leaves every other cell alone, so it is safe whatever Herdr shows
// there. It also ends the watch that Draw started.
func (p *Pane) EraseBlank(x, y, w, h int) error {
	if w < 1 || h < 1 {
		return nil
	}
	return p.send('E', rectBytes(x, y, w, h))
}

// send writes one message to the helper: a type byte, a length, the payload.
func (p *Pane) send(kind byte, payload []byte) error {
	n := len(payload)
	msg := append([]byte{kind, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}, payload...)
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.out.Write(msg)
	return err
}

// read carries out the helper's reports until it exits: 'w' when it finds the
// image wiped, or 's' and the terminal's columns and rows as two
// little-endian uint16s. sized is closed at the first size.
func (p *Pane) read(reports io.Reader, sized chan struct{}) {
	r := bufio.NewReader(reports)
	for {
		kind, err := r.ReadByte()
		if err != nil {
			return
		}
		switch kind {
		case 'w':
			select {
			case p.wiped <- struct{}{}:
			default:
			}
		case 's':
			var b [4]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return
			}
			p.mu.Lock()
			p.cols, p.rows = int(b[0])|int(b[1])<<8, int(b[2])|int(b[3])<<8
			p.mu.Unlock()
			if sized != nil {
				close(sized)
				sized = nil
			} else {
				p.refresh()
			}
		}
	}
}

func (p *Pane) refresh() {
	st, err := p.query()
	if err != nil {
		return
	}
	p.mu.Lock()
	changed := st != p.state
	p.state = st
	p.mu.Unlock()
	if changed {
		select {
		case p.changes <- struct{}{}:
		default:
		}
	}
}

// poll catches changes that send no event.
func (p *Pane) poll() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			p.refresh()
		case <-p.done:
			return
		}
	}
}

// watch refreshes the state whenever Herdr reports a change of view.
func (p *Pane) watch() {
	for {
		p.subscribe()
		select {
		case <-p.done:
			return
		case <-time.After(time.Second): // the connection dropped; try again
		}
	}
}

func (p *Pane) subscribe() {
	conn, err := dial(p.socket)
	if err != nil {
		return
	}
	defer conn.Close()
	go func() {
		<-p.done
		conn.Close()
	}()
	if _, err := conn.Write(subscription()); err != nil {
		return
	}
	lines := bufio.NewScanner(conn)
	lines.Buffer(make([]byte, 1<<20), 1<<24)
	for lines.Scan() {
		p.refresh()
	}
}

// events are the events after which the pane's place or showing may differ.
var events = []string{
	"layout.updated", "tab.focused", "tab.closed", "tab.moved", "pane.focused", "pane.moved",
	"pane.closed", "pane.created", "workspace.focused", "workspace.closed",
}

// subscription is the request line that subscribes to events.
func subscription() []byte {
	var subs []map[string]string
	for _, t := range events {
		subs = append(subs, map[string]string{"type": t})
	}
	req, _ := json.Marshal(map[string]any{
		"id": "slate", "method": "events.subscribe", "params": map[string]any{"subscriptions": subs},
	})
	return append(req, '\n')
}

// query asks Herdr for a snapshot and finds this pane in it.
func (p *Pane) query() (State, error) {
	conn, err := dial(p.socket)
	if err != nil {
		return State{}, err
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, `{"id":"slate","method":"session.snapshot","params":{}}`+"\n"); err != nil {
		return State{}, err
	}
	var reply struct {
		Error  *struct{ Message string }
		Result struct {
			Snapshot struct {
				FocusedWorkspace string `json:"focused_workspace_id"`
				FocusedTab       string `json:"focused_tab_id"`
				FocusedPane      string `json:"focused_pane_id"`
				Layouts          []struct {
					TabID       string `json:"tab_id"`
					WorkspaceID string `json:"workspace_id"`
					Zoomed      bool
					Area        rect
					FocusedPane string `json:"focused_pane_id"`
					Panes       []struct {
						PaneID string `json:"pane_id"`
						Rect   rect
					}
				}
			}
		}
	}
	lines := bufio.NewReader(conn)
	line, err := lines.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return State{}, err
	}
	if err := json.Unmarshal(line, &reply); err != nil {
		return State{}, err
	}
	if reply.Error != nil {
		return State{}, errors.New("herdr: " + reply.Error.Message)
	}
	p.mu.Lock()
	cols, rows := p.cols, p.rows
	p.mu.Unlock()
	snap := reply.Result.Snapshot
	for _, l := range snap.Layouts {
		for _, pane := range l.Panes {
			if pane.PaneID != p.id {
				continue
			}
			r := pane.Rect
			st := State{}
			st.Visible = l.WorkspaceID == snap.FocusedWorkspace && l.TabID == snap.FocusedTab
			if l.Zoomed {
				// A zoomed pane fills the tab and hides the others.
				st.Visible = st.Visible && l.FocusedPane == p.id
				r = l.Area
			}
			var ok bool
			st.X, st.Y, ok = place(cols, rows, l.Area, r)
			st.W, st.H = r.Width, r.Height
			st.Visible = st.Visible && ok
			st.Focused = st.Visible && snap.FocusedPane == p.id
			return st, nil
		}
	}
	// The pane is in no layout, as when its tab is not laid out yet.
	return State{}, nil
}

// rect is a rectangle of cells as Herdr's API reports it.
type rect struct{ X, Y, Width, Height int }

// place returns where r, a rectangle in the pane area, sits in a terminal of
// cols by rows cells. The pane area fills the bottom right of the terminal:
// the sidebar is to its left and the tab bar above it. Herdr counts area and r
// from the area's top left, so area is at 0,0. An area counted from the
// terminal's top left, as older versions reported it, gives the same answer.
// ok is false when the size is not known yet or does not fit, as in the middle
// of a resize.
func place(cols, rows int, area, r rect) (x, y int, ok bool) {
	ox, oy := cols-area.Width-area.X, rows-area.Height-area.Y
	if cols < 1 || rows < 1 || ox < 0 || oy < 0 {
		return 0, 0, false
	}
	return r.X + ox, r.Y + oy, true
}
