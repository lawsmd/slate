package slate

import (
	"bytes"
	"encoding/json"
	"image"
	"image/draw"
	"image/png"
	"strconv"
)

// frame is one decoded screencast frame: RGBA pixels, row y at pix[y*stride].
type frame struct {
	pix          []byte
	stride, w, h int
}

// frames takes the page's screencast frames until the page or the editor
// stops.
func (e *Editor) frames(pg *page, ch <-chan json.RawMessage) {
	for {
		select {
		case raw := <-ch:
			e.receive(pg, raw)
		case <-pg.conn.Done():
			return
		case <-pg.crashed:
			return
		case <-e.done:
			return
		}
	}
}

// receive acknowledges and decodes one screencast frame, then signals
// Updates. The browser sends no further frame until the last is acknowledged.
func (e *Editor) receive(pg *page, raw json.RawMessage) {
	var msg struct {
		Data      []byte `json:"data"`
		SessionID int    `json:"sessionId"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	pg.conn.Send(pg.sess, "Page.screencastFrameAck", map[string]any{"sessionId": msg.SessionID})
	img, err := png.Decode(bytes.NewReader(msg.Data))
	if err != nil {
		return
	}
	var f frame
	size := img.Bounds().Size()
	switch m := img.(type) {
	case *image.RGBA:
		f = frame{m.Pix, m.Stride, size.X, size.Y}
	case *image.NRGBA: // frames are opaque, so the bytes are the same
		f = frame{m.Pix, m.Stride, size.X, size.Y}
	default:
		rgba := image.NewRGBA(image.Rectangle{Max: size})
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		f = frame{rgba.Pix, rgba.Stride, size.X, size.Y}
	}
	e.mu.Lock()
	e.frame = f
	e.mu.Unlock()
	select {
	case e.updates <- struct{}{}:
	default:
	}
}

// Updates signals that the editor's picture changed and Draw has something
// to return. Signals are merged: one may stand for several changes.
func (e *Editor) Updates() <-chan struct{} { return e.updates }

// Draw returns the escape sequences that bring the terminal up to date with
// the editor, or nil when the block already shows the newest frame. It sends
// only the cell rows that changed since the last call. The sequences save
// and restore the cursor, so a host that tracks the cursor is not disturbed.
//
// The host must leave the block's cells alone between calls: text written
// over a cell erases the image there. After anything else has drawn over
// the block, call Invalidate and Draw again.
func (e *Editor) Draw() []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	f := e.frame
	if f.pix == nil {
		return nil
	}
	w := min(f.w, e.cols*e.cellW)
	rows := min(e.rows, f.h/e.cellH)
	rowBytes := e.cellH * f.stride
	same := len(e.shown) == len(f.pix)

	var out []byte
	changed := func(r int) bool {
		return !same || !bytes.Equal(f.pix[r*rowBytes:(r+1)*rowBytes], e.shown[r*rowBytes:(r+1)*rowBytes])
	}
	for r := 0; r < rows; {
		if !changed(r) {
			r++
			continue
		}
		end := r + 1
		for end < rows && changed(end) {
			end++
		}
		if out == nil {
			out = append(out, "\x1b7"...)
		}
		out = append(out, "\x1b["...)
		out = strconv.AppendInt(out, int64(e.row+r), 10)
		out = append(out, ';')
		out = strconv.AppendInt(out, int64(e.col), 10)
		out = append(out, 'H')
		out = e.enc.Encode(out, f.pix[r*rowBytes:], f.stride, w, (end-r)*e.cellH)
		r = end
	}
	if out == nil {
		return nil
	}
	e.shown = append(e.shown[:0], f.pix...)
	return append(out, "\x1b8"...)
}

// Invalidate makes the next Draw send the whole block. Call it after the
// host cleared the screen or drew over the block.
func (e *Editor) Invalidate() {
	e.mu.Lock()
	e.shown = nil
	e.mu.Unlock()
}

// SetRect moves and sizes the block. col and row are the terminal cell of
// its top-left corner, counted from 1. Draw uses the new place at once; the
// page follows a new size shortly after.
func (e *Editor) SetRect(col, row, cols, rows int) {
	e.mu.Lock()
	resized := cols != e.cols || rows != e.rows
	moved := col != e.col || row != e.row
	e.col, e.row, e.cols, e.rows = col, row, cols, rows
	if resized || moved {
		e.shown = nil
	}
	e.mu.Unlock()
	if resized {
		e.post("SetRect", e.resize)
	}
}

// SetCellSize sets the pixel size of one terminal cell. Ask the terminal for
// it with the escape sequence CSI 16 t.
func (e *Editor) SetCellSize(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	e.mu.Lock()
	changed := width != e.cellW || height != e.cellH
	e.cellW, e.cellH, e.shown = width, height, nil
	e.mu.Unlock()
	if changed {
		e.post("SetCellSize", e.resize)
	}
}
