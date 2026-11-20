package bubble

import (
	tea "charm.land/bubbletea/v2"

	"github.com/lawsmd/slate/herdr"
)

// Rect is a rectangle of terminal cells, counted from 0.
type Rect struct{ X, Y, W, H int }

// Kind says how much of the block a draw holds.
type Kind int

const (
	// Rows holds the cell rows that changed since the last draw.
	Rows Kind = iota
	// Whole holds every row, written over an image that is already there.
	Whole
	// Placed holds every row, for a place the image is not at: after a
	// move, or after something wiped it.
	Placed
)

// Painter puts the image on the screen. The model's default writes it
// through Bubble Tea. Another painter serves a host whose output does not
// reach the terminal unchanged, as in a multiplexer's pane.
type Painter interface {
	// Place turns a rectangle of the host's window, which is winW by winH
	// cells, into the cells the painter writes to. ok is false while the
	// block cannot be drawn.
	Place(winW, winH int, r Rect) (at Rect, ok bool)
	// Changes signals that Place may return something new. It is nil for
	// a painter whose answer never changes.
	Changes() <-chan struct{}
	// Draw writes seq, which the editor made for the place at.
	Draw(at Rect, seq []byte, kind Kind) tea.Cmd
	// Erase removes what is left of the image in a place it has left.
	Erase(at Rect)
	// Wiped signals that something else removed the image and a Placed
	// draw is needed. It is nil for a painter that cannot tell.
	Wiped() <-chan struct{}
	// CellSize returns the pixel size of one cell where the painter writes,
	// if the painter knows it. A detached painter's answer is the only one
	// used: the host's own terminal answers CSI 16 t for another screen.
	CellSize() (w, h int, ok bool)
	// Detached reports whether the image is written outside Bubble Tea's
	// output. Bubble Tea then neither orders its frames with the image nor
	// clears the image on exit.
	Detached() bool
}

// stdout writes the image through Bubble Tea, in the host's own cells.
type stdout struct{}

func (stdout) Place(_, _ int, r Rect) (Rect, bool)     { return r, true }
func (stdout) Changes() <-chan struct{}                { return nil }
func (stdout) Draw(_ Rect, seq []byte, _ Kind) tea.Cmd { return tea.Raw(string(seq)) }
func (stdout) Erase(Rect)                              {}
func (stdout) Wiped() <-chan struct{}                  { return nil }
func (stdout) CellSize() (int, int, bool)              { return 0, 0, false }
func (stdout) Detached() bool                          { return false }

// Herdr returns the painter for a host in a Herdr pane. It writes the image
// to the terminal that Herdr runs in, at the pane's place there. The caller
// closes the pane, after closing the model.
func Herdr(p *herdr.Pane) Painter { return herdrPainter{p} }

type herdrPainter struct{ pane *herdr.Pane }

func (h herdrPainter) Place(winW, winH int, r Rect) (Rect, bool) {
	st := h.pane.State()
	// Herdr's rectangle includes the border it draws round a pane that
	// shares its tab. The host's window is the inside.
	bw, bh := st.W-winW, st.H-winH
	if winW == 0 || (bw != 0 && bw != 2) || (bh != 0 && bh != 2) {
		// The pane was resized and one of the two sizes is not yet known.
		// Drawing now could put the image over another pane.
		return Rect{}, false
	}
	return Rect{st.X + bw/2 + r.X, st.Y + bh/2 + r.Y, r.W, r.H}, st.Visible
}

func (h herdrPainter) Changes() <-chan struct{} { return h.pane.Changes() }

func (h herdrPainter) Draw(at Rect, seq []byte, kind Kind) tea.Cmd {
	switch kind {
	case Rows:
		h.pane.Write(seq)
	case Whole:
		h.pane.Refresh(seq)
	case Placed:
		h.pane.Draw(at.X, at.Y, at.W, at.H, seq)
	}
	return nil
}

func (h herdrPainter) Erase(at Rect)              { h.pane.EraseBlank(at.X, at.Y, at.W, at.H) }
func (h herdrPainter) Wiped() <-chan struct{}     { return h.pane.Wiped() }
func (h herdrPainter) CellSize() (int, int, bool) { return h.pane.CellSize() }
func (h herdrPainter) Detached() bool             { return true }
