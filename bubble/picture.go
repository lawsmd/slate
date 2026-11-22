package bubble

import (
	"image"
	"image/color"
	"image/draw"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/lawsmd/slate"
	"github.com/lawsmd/slate/sixel"
)

// PictureConfig describes the block that OpenPicture makes.
type PictureConfig struct {
	// Background is the colour behind the picture, as "#rrggbb": it shows
	// through a transparent picture and fills the part of the last column
	// and row of cells the picture does not reach. Empty means black.
	Background string

	// Painter puts the image on the screen. Leave it nil and OpenPicture
	// chooses one for the terminal or multiplexer the program runs in, as
	// Open does. The caller closes a painter it sets here.
	Painter Painter
}

// Picture is a block that shows a still picture, scaled down to fit its
// cells. It is drawn as the editor's block is: the host shows blank cells
// where the picture goes and the picture is written over them as a Sixel
// image. It needs no browser.
//
// A host that scrolls gives Show the place of the whole picture and the part
// of its window the picture may be drawn in. The block draws the rows and
// columns of the picture that fall inside both.
//
// Create one with OpenPicture and pass Update every message the host does
// not consume itself. A Picture knows its own messages from another block's,
// so one program can hold many pictures and an editor.
type Picture struct {
	src   *image.RGBA // the picture over its background, with no transparency
	bg    color.RGBA
	paint Painter
	// closeHost closes the painter OpenPicture chose.
	closeHost func()
	done      chan struct{}

	whole, view  Rect  // as last given to Show, in window cells
	winW, winH   int   // the host's window in cells
	cellW, cellH int   // the pixel size of one cell
	covered      bool  // the host shows something over the block
	painted      *Rect // where the painter last wrote the image
	redraws      int   // counts calls to Redraw; only the newest one draws
	draws        int
	// latest is the number of the newest draw. An older one that is still
	// encoding when it ends writes nothing.
	latest atomic.Int64

	mu     sync.Mutex // guards canvas and enc, which draws use off the update loop
	canvas *image.RGBA
	enc    sixel.Encoder
}

// OpenPicture makes a block that shows img, ready for the terminal the host
// runs in. It finds the painter as Open does, and fails where Open would:
// under a host that drops the image.
func OpenPicture(img image.Image, cfg PictureConfig) (*Picture, error) {
	paint, closeHost := cfg.Painter, func() {}
	if paint == nil {
		var err error
		if paint, _, err = findPainter(); err != nil {
			return nil, err
		}
		if c, ok := paint.(interface{ Close() }); ok {
			closeHost = c.Close
		}
	}
	bg := parseColour(cfg.Background)
	b := img.Bounds()
	src := image.NewRGBA(image.Rectangle{Max: b.Size()})
	draw.Draw(src, src.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Over)
	return &Picture{
		src: src, bg: bg, paint: paint, closeHost: closeHost, done: make(chan struct{}),
		cellW: 10, cellH: 20,
	}, nil
}

// parseColour reads "#rrggbb". Anything else is black.
func parseColour(s string) color.RGBA {
	c := color.RGBA{A: 255}
	if len(s) != 7 || s[0] != '#' {
		return c
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return c
	}
	c.R, c.G, c.B = uint8(v>>16), uint8(v>>8), uint8(v)
	return c
}

type picKind int

const (
	picRedraw picKind = iota
	picWiped
	picOuter
	picErase
	picAgain
	picCell
)

// picMsg is a message of one picture.
type picMsg struct {
	p    *Picture
	kind picKind
	n    int  // the redraw's number, or a cell's width
	h    int  // a cell's height
	r    Rect // the place to erase
}

func (p *Picture) msg(kind picKind) picMsg { return picMsg{p: p, kind: kind} }

// Init asks for the size of a cell and starts listening to the painter.
func (p *Picture) Init() tea.Cmd {
	cmds := []tea.Cmd{func() tea.Msg {
		if w, h, ok := slate.EnvCellSize(); ok {
			return picMsg{p: p, kind: picCell, n: w, h: h}
		}
		if w, h, ok := p.paint.CellSize(); ok {
			return picMsg{p: p, kind: picCell, n: w, h: h}
		}
		return nil
	}}
	if p.asksTerminal() {
		cmds = append(cmds, tea.Raw(requestCellSize))
	}
	if p.paint.Changes() != nil {
		cmds = append(cmds, p.wait(p.paint.Changes(), picOuter))
	}
	if p.paint.Wiped() != nil {
		cmds = append(cmds, p.wait(p.paint.Wiped(), picWiped))
	}
	return tea.Batch(cmds...)
}

func (p *Picture) wait(ch <-chan struct{}, kind picKind) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-ch:
			return p.msg(kind)
		case <-p.done:
			return nil
		}
	}
}

// asksTerminal reports whether the answer to CSI 16 t is the size of the
// cells the image is drawn on, as it is for the editor's block.
func (p *Picture) asksTerminal() bool {
	_, _, fixed := slate.EnvCellSize()
	return !fixed && !p.paint.Detached()
}

// Fit returns the cells the picture takes when it is scaled down to fit in
// cols by rows cells. A picture that fits already is not enlarged. The
// answer changes with the size of a cell, which the terminal reports after
// Init, so a host asks again when it lays out.
func (p *Picture) Fit(cols, rows int) (w, h int) {
	if cols < 1 || rows < 1 {
		return 0, 0
	}
	fw, fh := fit(p.src.Rect.Dx(), p.src.Rect.Dy(), cols*p.cellW, rows*p.cellH)
	return min((fw+p.cellW-1)/p.cellW, cols), min((fh+p.cellH-1)/p.cellH, rows)
}

// fit scales sw by sh down to fit in bw by bh, keeping its shape.
func fit(sw, sh, bw, bh int) (w, h int) {
	if sw < 1 || sh < 1 {
		return 0, 0
	}
	if sw <= bw && sh <= bh {
		return sw, sh
	}
	if sw*bh > sh*bw {
		return bw, max(sh*bw/sw, 1)
	}
	return max(sw*bh/sh, 1), bh
}

// Show places the picture. whole is the cells the picture takes, in window
// cells counted from 0, and may reach outside the window. view is the part
// of the window the picture may be drawn in. The picture is scaled to whole
// and the part of it inside view is drawn; with none inside, nothing is. The
// host shows blank cells there, as for the editor's block.
func (p *Picture) Show(whole, view Rect) tea.Cmd {
	if whole == p.whole && view == p.view {
		return nil
	}
	p.whole, p.view = whole, view
	if _, visible := p.target(); !visible {
		return p.hide()
	}
	return p.Redraw()
}

// hide forgets where the picture was drawn. A detached painter also removes
// what is left of it there, and stops watching the place.
func (p *Picture) hide() tea.Cmd {
	old := p.painted
	p.painted = nil
	p.latest.Add(1)
	if old == nil || !p.paint.Detached() {
		return nil
	}
	return tea.Tick(eraseDelay, func(time.Time) tea.Msg { return picMsg{p: p, kind: picErase, r: *old} })
}

// visible is the part of the picture inside the view, in window cells.
func (p *Picture) visible() (Rect, bool) {
	x0, y0 := max(p.whole.X, p.view.X), max(p.whole.Y, p.view.Y)
	x1 := min(p.whole.X+p.whole.W, p.view.X+p.view.W)
	y1 := min(p.whole.Y+p.whole.H, p.view.Y+p.view.H)
	if x1 <= x0 || y1 <= y0 {
		return Rect{}, false
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}, true
}

// target returns the cells the painter draws the visible part in and
// whether it can be drawn now.
func (p *Picture) target() (Rect, bool) {
	vis, ok := p.visible()
	if !ok {
		return Rect{}, false
	}
	return p.paint.Place(p.winW, p.winH, vis)
}

// Redraw draws the picture again shortly after the current frame. Calls made
// close together end in one draw.
func (p *Picture) Redraw() tea.Cmd {
	p.redraws++
	n := p.redraws
	return tea.Tick(redrawDelay, func(time.Time) tea.Msg { return picMsg{p: p, kind: picRedraw, n: n} })
}

// Cover tells the block that the host shows something over it, or has
// stopped doing so, as Model.Cover does.
func (p *Picture) Cover(on bool) tea.Cmd {
	if p.covered == on {
		return nil
	}
	p.covered = on
	if on {
		p.latest.Add(1)
		return nil
	}
	return p.Redraw()
}

// Update handles the block's own messages and the ones about the window.
func (p *Picture) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case picMsg:
		if msg.p != p {
			return nil
		}
		switch msg.kind {
		case picRedraw:
			if msg.n == p.redraws {
				return p.draw()
			}
		case picWiped:
			if p.painted != nil {
				return tea.Batch(p.draw(), p.wait(p.paint.Wiped(), picWiped))
			}
			return p.wait(p.paint.Wiped(), picWiped)
		case picOuter:
			return tea.Batch(p.moved(), p.wait(p.paint.Changes(), picOuter))
		case picErase:
			p.paint.Erase(msg.r)
			if _, visible := p.target(); visible {
				return p.Redraw() // the erased cells may lie under the picture's new place
			}
		case picAgain:
			if p.painted != nil {
				return p.draw()
			}
		case picCell:
			return p.setCell(msg.n, msg.h)
		}
	case uv.CellSizeEvent:
		if p.asksTerminal() {
			return p.setCell(msg.Width, msg.Height)
		}
	case tea.WindowSizeMsg:
		p.winW, p.winH = msg.Width, msg.Height
		if p.paint.Detached() {
			return p.moved()
		}
		// A zoom changes the cell size and reports as a resize.
		if p.asksTerminal() {
			return tea.Raw(requestCellSize)
		}
	case tea.FocusMsg:
		// As for the editor's block: a detached image is most often lost
		// when the window gets the keyboard focus back.
		if !p.paint.Detached() {
			return nil
		}
		again := func(time.Time) tea.Msg { return p.msg(picAgain) }
		return tea.Batch(tea.Tick(200*time.Millisecond, again), tea.Tick(time.Second, again))
	}
	return nil
}

func (p *Picture) setCell(w, h int) tea.Cmd {
	if w < 1 || h < 1 {
		return nil
	}
	p.cellW, p.cellH = w, h
	if _, visible := p.target(); !visible {
		return nil
	}
	return p.Redraw()
}

// moved handles a change in where a detached painter draws the block or
// whether it shows.
func (p *Picture) moved() tea.Cmd {
	now, visible := p.target()
	if p.painted != nil && (!visible || *p.painted != now) {
		return p.hide() // the erase draws the picture again where it now is
	}
	if visible {
		return p.Redraw()
	}
	return nil
}

// draw writes the visible part of the picture. The scaling and encoding run
// in the command, off the host's update loop.
func (p *Picture) draw() tea.Cmd {
	if p.covered {
		return nil // Cover(false) draws
	}
	at, visible := p.target()
	if !visible {
		return nil
	}
	vis, _ := p.visible()
	whole, cw, ch := p.whole, p.cellW, p.cellH
	p.painted = &at
	p.draws++
	n := p.latest.Add(1)
	return func() tea.Msg {
		seq := p.render(whole, vis, at, cw, ch)
		if seq == nil || p.latest.Load() != n {
			return nil // the picture has moved since
		}
		if cmd := p.paint.Draw(at, seq, Placed); cmd != nil {
			return cmd()
		}
		return nil
	}
}

// Draws returns how many times the picture was written, for diagnostics.
func (p *Picture) Draws() int { return p.draws }

// render returns the escape sequences that draw the part vis of a picture
// that takes the cells whole, at the cells at of the painter's screen. The
// sequences save and restore the cursor.
func (p *Picture) render(whole, vis, at Rect, cw, ch int) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, h := whole.W*cw, whole.H*ch
	if w < 1 || h < 1 {
		return nil
	}
	if p.canvas == nil || p.canvas.Rect.Dx() != w || p.canvas.Rect.Dy() != h {
		p.canvas = image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(p.canvas, p.canvas.Bounds(), image.NewUniform(p.bg), image.Point{}, draw.Src)
		fw, fh := fit(p.src.Rect.Dx(), p.src.Rect.Dy(), w, h)
		shrink(p.canvas, fw, fh, p.src)
	}
	x, y := (vis.X-whole.X)*cw, (vis.Y-whole.Y)*ch
	out := []byte("\x1b7\x1b[")
	out = strconv.AppendInt(out, int64(at.Y+1), 10)
	out = append(out, ';')
	out = strconv.AppendInt(out, int64(at.X+1), 10)
	out = append(out, 'H')
	out = p.enc.Encode(out, p.canvas.Pix[y*p.canvas.Stride+x*4:], p.canvas.Stride, vis.W*cw, vis.H*ch)
	return append(out, "\x1b8"...)
}

// shrink draws src into the top left w by h pixels of dst. Each pixel is the
// mean of the pixels of src it stands for. w and h are no larger than src.
func shrink(dst *image.RGBA, w, h int, src *image.RGBA) {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	if w == sw && h == sh {
		draw.Draw(dst, image.Rect(0, 0, w, h), src, image.Point{}, draw.Src)
		return
	}
	for y := 0; y < h; y++ {
		y0 := y * sh / h
		y1 := max((y+1)*sh/h, y0+1)
		row := dst.Pix[y*dst.Stride:]
		for x := 0; x < w; x++ {
			x0 := x * sw / w
			x1 := max((x+1)*sw/w, x0+1)
			var r, g, b uint32
			for sy := y0; sy < y1; sy++ {
				px := src.Pix[sy*src.Stride+x0*4 : sy*src.Stride+x1*4]
				for i := 0; i < len(px); i += 4 {
					r += uint32(px[i])
					g += uint32(px[i+1])
					b += uint32(px[i+2])
				}
			}
			n := uint32((x1 - x0) * (y1 - y0))
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = uint8(r/n), uint8(g/n), uint8(b/n), 255
		}
	}
}

// Close stops the block's waiting commands. With a detached painter it also
// removes the image, which would stay behind, and that takes a moment: a
// host that closes several pictures can close them at once, each in its own
// goroutine.
func (p *Picture) Close() {
	close(p.done)
	p.latest.Add(1)
	if p.paint.Detached() && p.painted != nil {
		time.Sleep(eraseDelay)
		p.paint.Erase(*p.painted)
	}
	p.closeHost()
}
