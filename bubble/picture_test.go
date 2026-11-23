package bubble

import (
	"bytes"
	"image"
	"image/color"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// recorder is a painter that keeps what it is asked to draw and counts the
// times it is told the block leaves its place.
type recorder struct {
	stdout
	at     []Rect
	seqs   [][]byte
	leaves int
}

func (r *recorder) Leave() { r.leaves++ }

func (r *recorder) Draw(at Rect, seq []byte, _ Kind) tea.Cmd {
	r.at = append(r.at, at)
	r.seqs = append(r.seqs, seq)
	return nil
}

func testPicture(t *testing.T, w, h int) (*Picture, *recorder) {
	t.Setenv("SLATE_CELL", "")
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	rec := &recorder{}
	p, err := OpenPicture(img, PictureConfig{Painter: rec})
	if err != nil {
		t.Fatal(err)
	}
	return p, rec
}

// run carries out a command and the block's own messages that follow from
// it, skipping the waits.
func run(p *Picture, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				run(p, c)
			}
			return
		}
		if _, ok := msg.(picMsg); ok {
			run(p, p.Update(msg))
		}
	case <-time.After(time.Second):
	}
}

func TestPictureFitsItsCells(t *testing.T) {
	// Cells are 10 by 20 pixels until the terminal says otherwise.
	for _, c := range []struct {
		name             string
		w, h, cols, rows int
		wantW, wantH     int
	}{
		{"a small picture is not enlarged", 95, 30, 80, 24, 10, 2},
		{"a wide picture is held to the columns", 1600, 400, 80, 24, 80, 10},
		{"a tall picture is held to the rows", 400, 1920, 80, 24, 10, 24},
		{"no room", 400, 400, 0, 24, 0, 0},
	} {
		p, _ := testPicture(t, c.w, c.h)
		if w, h := p.Fit(c.cols, c.rows); w != c.wantW || h != c.wantH {
			t.Errorf("%s: got %d by %d cells, want %d by %d", c.name, w, h, c.wantW, c.wantH)
		}
	}
}

func TestPictureDrawsThePartInView(t *testing.T) {
	p, rec := testPicture(t, 400, 200) // 40 by 10 cells
	// The top four rows of the picture are above the view.
	run(p, p.Show(Rect{X: 5, Y: -4, W: 40, H: 10}, Rect{X: 0, Y: 0, W: 100, H: 30}))
	if len(rec.at) != 1 || rec.at[0] != (Rect{X: 5, Y: 0, W: 40, H: 6}) {
		t.Fatalf("drawn at %+v, want the six rows in view", rec.at)
	}
	seq := rec.seqs[0]
	if !bytes.HasPrefix(seq, []byte("\x1b7\x1b[1;6H\x1bP")) || !bytes.HasSuffix(seq, []byte("\x1b\\\x1b8")) {
		t.Errorf("the sequence does not place the image at row 1, column 6: %q...", seq[:min(len(seq), 24)])
	}
	if !bytes.Contains(seq, []byte(`"1;1;400;120`)) {
		t.Errorf("the image is not 400 by 120 pixels: %q...", seq[:min(len(seq), 40)])
	}

	// Out of view it draws nothing more.
	run(p, p.Show(Rect{X: 5, Y: -12, W: 40, H: 10}, Rect{X: 0, Y: 0, W: 100, H: 30}))
	if len(rec.at) != 1 {
		t.Errorf("a picture out of view was drawn at %+v", rec.at[1:])
	}
}

func TestPictureStaysCoveredUntilUncovered(t *testing.T) {
	p, rec := testPicture(t, 100, 100)
	p.Cover(true)
	run(p, p.Show(Rect{W: 10, H: 5}, Rect{W: 80, H: 24}))
	if len(rec.at) != 0 {
		t.Fatal("a covered picture was drawn")
	}
	run(p, p.Cover(false))
	if len(rec.at) != 1 {
		t.Fatalf("drawn %d times after the cover went, want once", len(rec.at))
	}
}

func TestPictureLeavesAnotherPicturesMessagesAlone(t *testing.T) {
	a, rec := testPicture(t, 100, 100)
	b, _ := testPicture(t, 100, 100)
	a.Show(Rect{W: 10, H: 5}, Rect{W: 80, H: 24})
	if cmd := a.Update(picMsg{p: b, kind: picRedraw, n: a.redraws}); cmd != nil {
		run(a, cmd)
	}
	if len(rec.at) != 0 {
		t.Error("a picture drew for another picture's message")
	}
}

func TestShrinkKeepsThePictureOverItsBackground(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+3] = 200, 255
	}
	dst := image.NewRGBA(image.Rect(0, 0, 3, 3))
	shrink(dst, 2, 2, src)
	if got := dst.RGBAAt(1, 1); got != (color.RGBA{R: 200, A: 255}) {
		t.Errorf("a pixel of the picture is %+v", got)
	}
	if got := dst.RGBAAt(2, 2); got != (color.RGBA{}) {
		t.Errorf("a pixel outside the picture was written: %+v", got)
	}
}

func TestMovedPictureLeavesItsPlaceBeforeItDraws(t *testing.T) {
	p, rec := testPicture(t, 400, 200) // 40 by 10 cells
	view := Rect{X: 0, Y: 0, W: 100, H: 30}
	run(p, p.Show(Rect{X: 5, Y: 12, W: 40, H: 10}, view))
	if len(rec.at) != 1 || rec.leaves != 0 {
		t.Fatalf("after the first Show: %d draws and %d leaves, want 1 and 0", len(rec.at), rec.leaves)
	}

	// The host scrolls. The painter hears of it before the host's frame
	// blanks the cells, and nothing draws until the redraw.
	redraw := p.Show(Rect{X: 5, Y: 9, W: 40, H: 10}, view)
	if rec.leaves != 1 {
		t.Fatalf("%d leaves after the move, want 1", rec.leaves)
	}
	if cmd := p.Update(p.msg(picAgain)); cmd != nil || len(rec.at) != 1 {
		t.Fatalf("a draw between the move and its redraw: %d draws", len(rec.at))
	}
	run(p, redraw)
	if len(rec.at) != 2 || rec.at[1] != (Rect{X: 5, Y: 9, W: 40, H: 10}) {
		t.Fatalf("drawn at %+v, want the new place last", rec.at)
	}
	if cmd := p.Update(p.msg(picAgain)); cmd == nil {
		t.Fatal("no draw after the redraw")
	}

	// The same place again is no move.
	p.Show(Rect{X: 5, Y: 9, W: 40, H: 10}, view)
	if rec.leaves != 1 {
		t.Fatalf("%d leaves after a Show that changed nothing, want 1", rec.leaves)
	}
}

func TestPictureDrawStartedBeforeAMoveWritesNothing(t *testing.T) {
	p, rec := testPicture(t, 400, 200)
	view := Rect{X: 0, Y: 0, W: 100, H: 30}
	tick := p.Show(Rect{X: 5, Y: 12, W: 40, H: 10}, view)
	encode := p.Update(tick()) // the draw, not yet scaled or encoded
	if encode == nil {
		t.Fatal("the redraw started no draw")
	}
	p.Show(Rect{X: 5, Y: 9, W: 40, H: 10}, view)
	if msg := encode(); msg != nil || len(rec.at) != 0 {
		t.Fatalf("the draw for the old place wrote %+v", rec.at)
	}
}

func TestCoveredPictureLeavesItsPlace(t *testing.T) {
	p, rec := testPicture(t, 400, 200)
	p.Cover(true)
	if rec.leaves != 1 {
		t.Fatalf("%d leaves, want 1", rec.leaves)
	}
}
