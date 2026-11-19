// Command slate-bench measures the delay between a keystroke and the redrawn
// Sixel frame: headless Edge, one editable page, a fixed rectangle.
//
// Run in Windows Terminal, it draws the frames and waits for the terminal to
// acknowledge each one. Run with output redirected, it times capture, decode
// and encode only.
package main

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/lawsmd/slate/browser"
	"github.com/lawsmd/slate/sixel"
	"github.com/lawsmd/slate/term"
)

//go:embed page.html
var pageHTML string

const sample = "the quick brown fox jumps over the lazy dog. "

var (
	flagCols   = flag.Int("cols", 80, "width of the rectangle in cells")
	flagRows   = flag.Int("rows", 24, "height of the rectangle in cells")
	flagN      = flag.Int("n", 200, "keystrokes per mode")
	flagGap    = flag.Duration("gap", 20*time.Millisecond, "pause between synthetic keystrokes")
	flagMode   = flag.String("mode", "", "one of shot-full, shot-dirty, cast-full, cast-dirty; empty runs all four (shot-dirty with -manual)")
	flagManual = flag.Bool("manual", false, "type by hand; ctrl+c quits")
	flagGPU    = flag.Bool("gpu", false, "leave GPU compositing on")
	flagEdge   = flag.String("edge", "", "browser executable; empty looks for Edge, then Chrome")
	flagOut    = flag.String("out", "", "also write the results to this file as JSON")
	flagCell   = flag.String("cell", "10x20", "cell size in pixels, used when the terminal does not report one")
)

var modes = []string{"shot-full", "shot-dirty", "cast-full", "cast-dirty"}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "slate-bench:", err)
		os.Exit(1)
	}
}

// timing is the cost of one keystroke, stage by stage.
type timing struct {
	capture, decode, encode, write, ack, total time.Duration
	bytes                                      int
	changed                                    bool
}

type modeResult struct {
	Mode      string             `json:"mode"`
	Keys      int                `json:"keys"`
	Unchanged int                `json:"unchanged"` // frames that showed no change after a key
	Missed    int                `json:"missed"`    // keys for which no frame arrived
	AvgBytes  int                `json:"avgBytes"`
	MedianMS  map[string]float64 `json:"medianMs"`
	P95MS     map[string]float64 `json:"p95Ms"`
}

type results struct {
	Browser  string       `json:"browser"`
	GPU      bool         `json:"gpu"`
	Terminal bool         `json:"terminal"`
	Sixel    bool         `json:"sixelReported"`
	Attrs    string       `json:"deviceAttributes"`
	Cols     int          `json:"cols"`
	Rows     int          `json:"rows"`
	CellW    int          `json:"cellW"`
	CellH    int          `json:"cellH"`
	Modes    []modeResult `json:"modes"`
}

type bench struct {
	conn *browser.Conn
	sess string
	tty  *term.Term // nil when not run in a terminal
	out  io.Writer

	col, row     int // top-left cell of the rectangle, 1-based
	cols, rows   int
	cellW, cellH int

	enc  sixel.Encoder
	prev []byte
	buf  []byte
	cast <-chan json.RawMessage
}

func run() error {
	b := &bench{out: io.Discard, col: 2, row: 2, cols: *flagCols, rows: *flagRows}
	res := results{GPU: *flagGPU}
	fmt.Sscanf(*flagCell, "%dx%d", &b.cellW, &b.cellH)
	if b.cellW <= 0 || b.cellH <= 0 {
		return fmt.Errorf("bad -cell %q", *flagCell)
	}

	if term.IsTerminal() {
		tty, err := term.Open()
		if err != nil {
			return err
		}
		defer tty.Close()
		b.tty, b.out, res.Terminal = tty, tty, true
		res.Attrs, res.Sixel = tty.DeviceAttributes()
		if w, h, ok := tty.CellSize(); ok {
			b.cellW, b.cellH = w, h
		}
		// Leave a row above for the title and two below for the status line.
		tc, tr := tty.Size()
		b.cols, b.rows = min(b.cols, tc-2), min(b.rows, tr-4)
		if b.cols < 20 || b.rows < 5 {
			return fmt.Errorf("terminal too small: %dx%d cells", tc, tr)
		}
		fmt.Fprint(tty, "\x1b[?1049h\x1b[?25l\x1b[2J")
		defer fmt.Fprint(tty, "\x1b[?25h\x1b[?1049l")
	} else if *flagManual {
		return errors.New("-manual needs a terminal")
	}
	res.Cols, res.Rows, res.CellW, res.CellH = b.cols, b.rows, b.cellW, b.cellH

	b.status(1, "slate-bench: starting the browser")
	br, err := browser.Launch(browser.Options{Path: *flagEdge, GPU: *flagGPU})
	if err != nil {
		return err
	}
	defer br.Close()
	res.Browser = br.Version()
	b.conn = br.Conn
	if b.sess, err = br.NewPage(); err != nil {
		return err
	}
	if err := b.load(); err != nil {
		return err
	}

	if *flagManual {
		mode := *flagMode
		if mode == "" {
			mode = "shot-dirty"
		}
		return b.manual(mode)
	}

	run := modes
	if *flagMode != "" {
		run = []string{*flagMode}
	}
	for _, mode := range run {
		r, err := b.synthetic(mode)
		if err != nil {
			return fmt.Errorf("%s: %w", mode, err)
		}
		res.Modes = append(res.Modes, r)
	}

	if b.tty != nil {
		fmt.Fprint(b.tty, "\x1b[?25h\x1b[?1049l")
		b.tty.Close()
		b.tty = nil
	}
	report(os.Stdout, res)
	if *flagOut != "" {
		data, _ := json.MarshalIndent(res, "", "  ")
		return os.WriteFile(*flagOut, data, 0o644)
	}
	return nil
}

// load sizes the page to the rectangle and loads the test document.
func (b *bench) load() error {
	loaded := b.conn.Subscribe(b.sess, "Page.loadEventFired", 4)
	b.cast = b.conn.Subscribe(b.sess, "Page.screencastFrame", 64)
	steps := []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Emulation.setDeviceMetricsOverride", map[string]any{
			"width": b.cols * b.cellW, "height": b.rows * b.cellH, "deviceScaleFactor": 1, "mobile": false}},
		{"Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true}},
		{"Page.navigate", map[string]any{
			"url": "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(pageHTML))}},
	}
	for _, s := range steps {
		if err := b.conn.Call(b.sess, s.method, s.params, nil); err != nil {
			return err
		}
	}
	select {
	case <-loaded:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("the test page did not load")
	}
}

// reset reloads the page so that every mode starts from the same document.
func (b *bench) reset(mode string) error {
	b.conn.Call(b.sess, "Page.stopScreencast", nil, nil)
	if err := b.load(); err != nil {
		return err
	}
	b.prev = nil
	if strings.HasPrefix(mode, "cast-") {
		err := b.conn.Call(b.sess, "Page.startScreencast", map[string]any{"format": "png", "everyNthFrame": 1}, nil)
		if err != nil {
			return err
		}
		// The first frame arrives without a key press.
		if data, err := b.castFrame(2 * time.Second); err == nil {
			b.show(data, false)
		}
		return nil
	}
	data, err := b.shot()
	if err != nil {
		return err
	}
	_, err = b.show(data, false)
	return err
}

func (b *bench) synthetic(mode string) (modeResult, error) {
	r := modeResult{Mode: mode}
	if !slices.Contains(modes, mode) {
		return r, errors.New("unknown mode")
	}
	if err := b.reset(mode); err != nil {
		return r, err
	}
	var ts []timing
	for i := 0; i < *flagN; i++ {
		b.status(1, fmt.Sprintf("slate-bench: %s, key %d of %d", mode, i+1, *flagN))
		t, err := b.keystroke(mode, []term.Event{{Kind: term.Text, Rune: rune(sample[i%len(sample)])}})
		switch {
		case errors.Is(err, errNoFrame):
			r.Missed++
		case err != nil:
			return r, err
		default:
			ts = append(ts, t)
			if !t.changed {
				r.Unchanged++
			}
		}
		time.Sleep(*flagGap)
	}
	r.Keys = len(ts)
	r.MedianMS, r.P95MS = map[string]float64{}, map[string]float64{}
	total := 0
	for _, t := range ts {
		total += t.bytes
	}
	if len(ts) > 0 {
		r.AvgBytes = total / len(ts)
	}
	for name, get := range stages {
		r.MedianMS[name], r.P95MS[name] = percentiles(ts, get)
	}
	return r, nil
}

var stages = map[string]func(timing) time.Duration{
	"capture": func(t timing) time.Duration { return t.capture },
	"decode":  func(t timing) time.Duration { return t.decode },
	"encode":  func(t timing) time.Duration { return t.encode },
	"write":   func(t timing) time.Duration { return t.write },
	"ack":     func(t timing) time.Duration { return t.ack },
	"total":   func(t timing) time.Duration { return t.total },
}

var stageOrder = []string{"capture", "decode", "encode", "write", "ack", "total"}

func percentiles(ts []timing, get func(timing) time.Duration) (median, p95 float64) {
	if len(ts) == 0 {
		return 0, 0
	}
	d := make([]time.Duration, len(ts))
	for i, t := range ts {
		d[i] = get(t)
	}
	slices.Sort(d)
	ms := func(x time.Duration) float64 { return float64(x.Microseconds()) / 1000 }
	return ms(d[len(d)/2]), ms(d[min(len(d)-1, len(d)*95/100)])
}

var errNoFrame = errors.New("no frame arrived")

// keystroke sends the keys, captures the frame that follows, and draws it.
// The total runs from the first key event to the terminal's acknowledgement.
func (b *bench) keystroke(mode string, keys []term.Event) (timing, error) {
	var t timing
	cast := strings.HasPrefix(mode, "cast-")
	if cast {
		b.drainCast()
	}
	start := time.Now()
	for _, k := range keys {
		if err := b.key(k); err != nil {
			return t, err
		}
	}
	var data []byte
	var err error
	if cast {
		data, err = b.castFrame(500 * time.Millisecond)
	} else {
		data, err = b.shot()
	}
	if err != nil {
		return t, err
	}
	t.capture = time.Since(start)
	t, err = b.showTimed(t, data, strings.HasSuffix(mode, "-dirty"))
	t.total = time.Since(start)
	return t, err
}

func (b *bench) shot() ([]byte, error) {
	var res struct {
		Data []byte `json:"data"`
	}
	err := b.conn.Call(b.sess, "Page.captureScreenshot",
		map[string]any{"format": "png", "optimizeForSpeed": true}, &res)
	return res.Data, err
}

type castFrame struct {
	Data      []byte `json:"data"`
	SessionID int    `json:"sessionId"`
}

// castFrame waits for the next screencast frame and acknowledges it. The
// browser sends no further frame until the last one is acknowledged.
func (b *bench) castFrame(timeout time.Duration) ([]byte, error) {
	select {
	case raw := <-b.cast:
		var f castFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		b.conn.Send(b.sess, "Page.screencastFrameAck", map[string]any{"sessionId": f.SessionID})
		return f.Data, nil
	case <-time.After(timeout):
		return nil, errNoFrame
	}
}

func (b *bench) drainCast() {
	for {
		if _, err := b.castFrame(0); err != nil {
			return
		}
	}
}

func (b *bench) show(data []byte, dirty bool) (timing, error) {
	return b.showTimed(timing{}, data, dirty)
}

// showTimed decodes a PNG frame, encodes the rows to draw and writes them.
// With dirty set it draws only the cell rows that differ from the last frame.
func (b *bench) showTimed(t timing, data []byte, dirty bool) (timing, error) {
	mark := time.Now()
	pix, stride, w, h, err := decode(data)
	if err != nil {
		return t, err
	}
	t.decode = time.Since(mark)

	mark = time.Now()
	w = min(w, b.cols*b.cellW)
	rows := min(b.rows, h/b.cellH)
	rowBytes := b.cellH * stride
	same := len(b.prev) == len(pix)
	b.buf = append(b.buf[:0], "\x1b[?2026h"...)
	for r := 0; r < rows; {
		rowChanged := func(r int) bool {
			return !same || !bytes.Equal(pix[r*rowBytes:(r+1)*rowBytes], b.prev[r*rowBytes:(r+1)*rowBytes])
		}
		if !rowChanged(r) {
			r++
			continue
		}
		t.changed = true
		end := r + 1
		for end < rows && rowChanged(end) {
			end++
		}
		if !dirty {
			r, end = 0, rows
		}
		b.buf = fmt.Appendf(b.buf, "\x1b[%d;%dH", b.row+r, b.col)
		b.buf = b.enc.Encode(b.buf, pix[r*rowBytes:], stride, w, (end-r)*b.cellH)
		r = end
	}
	b.buf = append(b.buf, "\x1b[?2026l"...)
	b.prev = append(b.prev[:0], pix...)
	t.encode = time.Since(mark)
	t.bytes = len(b.buf)

	mark = time.Now()
	if _, err := b.out.Write(b.buf); err != nil {
		return t, err
	}
	t.write = time.Since(mark)

	if b.tty != nil {
		mark = time.Now()
		// The cursor position report comes back once the terminal has read
		// the image.
		if _, ok := b.tty.Query("\x1b[6n", 'R', 3*time.Second); !ok {
			return t, errors.New("the terminal did not acknowledge the frame")
		}
		t.ack = time.Since(mark)
	}
	return t, nil
}

func decode(data []byte) (pix []byte, stride, w, h int, err error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, 0, err
	}
	size := img.Bounds().Size()
	switch m := img.(type) {
	case *image.RGBA:
		return m.Pix, m.Stride, size.X, size.Y, nil
	case *image.NRGBA:
		return m.Pix, m.Stride, size.X, size.Y, nil // screenshots are opaque
	}
	rgba := image.NewRGBA(image.Rectangle{Max: size})
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	return rgba.Pix, rgba.Stride, size.X, size.Y, nil
}

type keyDef struct {
	key, code string
	vk        int
	text      string
}

var keyDefs = map[term.Kind]keyDef{
	term.Enter:     {"Enter", "Enter", 13, "\r"},
	term.Backspace: {"Backspace", "Backspace", 8, ""},
	term.Tab:       {"Tab", "Tab", 9, ""},
	term.Left:      {"ArrowLeft", "ArrowLeft", 37, ""},
	term.Up:        {"ArrowUp", "ArrowUp", 38, ""},
	term.Right:     {"ArrowRight", "ArrowRight", 39, ""},
	term.Down:      {"ArrowDown", "ArrowDown", 40, ""},
	term.Home:      {"Home", "Home", 36, ""},
	term.End:       {"End", "End", 35, ""},
	term.Delete:    {"Delete", "Delete", 46, ""},
}

// key sends one key press to the page. It waits for the key-down to be
// handled, so that a screenshot taken next includes the edit.
func (b *bench) key(e term.Event) error {
	down := map[string]any{"type": "keyDown"}
	up := map[string]any{"type": "keyUp"}
	if e.Kind == term.Text {
		s := string(e.Rune)
		down["key"], down["text"], up["key"] = s, s, s
	} else {
		d, ok := keyDefs[e.Kind]
		if !ok {
			return nil
		}
		for _, m := range []map[string]any{down, up} {
			m["key"], m["code"], m["windowsVirtualKeyCode"] = d.key, d.code, d.vk
		}
		if d.text != "" {
			down["text"] = d.text
		} else {
			down["type"] = "rawKeyDown"
		}
	}
	if err := b.conn.Call(b.sess, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return b.conn.Send(b.sess, "Input.dispatchKeyEvent", up)
}

// manual redraws after each key typed by hand and shows running timings.
func (b *bench) manual(mode string) error {
	if !slices.Contains(modes, mode) {
		return errors.New("unknown mode " + mode)
	}
	if err := b.reset(mode); err != nil {
		return err
	}
	b.status(1, "slate-bench: "+mode+". Type in the block. ctrl+c quits.")
	var ts []timing
	for e := range b.tty.Events {
		keys := []term.Event{e}
		// Keys typed while the last frame was drawn share one redraw.
	pending:
		for {
			select {
			case next, ok := <-b.tty.Events:
				if !ok {
					break pending
				}
				keys = append(keys, next)
			default:
				break pending
			}
		}
		if slices.ContainsFunc(keys, func(e term.Event) bool { return e.Kind == term.CtrlC }) {
			return nil
		}
		t, err := b.keystroke(mode, keys)
		if errors.Is(err, errNoFrame) {
			continue
		}
		if err != nil {
			return err
		}
		ts = append(ts, t)
		if len(ts) > 200 {
			ts = ts[1:]
		}
		median, p95 := percentiles(ts, stages["total"])
		b.status(b.row+b.rows+1, fmt.Sprintf(
			"last %5.1f ms (capture %.1f, decode %.1f, encode %.1f, write %.1f, ack %.1f)   median %.1f   p95 %.1f   %d KB",
			ms(t.total), ms(t.capture), ms(t.decode), ms(t.encode), ms(t.write), ms(t.ack), median, p95, t.bytes/1024))
	}
	return nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// status writes one line of text at the given terminal row.
func (b *bench) status(row int, s string) {
	if b.tty != nil {
		fmt.Fprintf(b.tty, "\x1b[%d;1H\x1b[2K%s", row, s)
	}
}

func report(w io.Writer, res results) {
	fmt.Fprintf(w, "browser %s, gpu %v\n", res.Browser, res.GPU)
	fmt.Fprintf(w, "rectangle %dx%d cells of %dx%d px = %dx%d px\n",
		res.Cols, res.Rows, res.CellW, res.CellH, res.Cols*res.CellW, res.Rows*res.CellH)
	if res.Terminal {
		fmt.Fprintf(w, "terminal attributes %q, Sixel reported: %v\n", res.Attrs, res.Sixel)
	} else {
		fmt.Fprintln(w, "not a terminal: frames were encoded and discarded, so write and ack are not measured")
	}
	fmt.Fprintf(w, "\n%-11s %5s %6s %6s %7s", "mode", "keys", "unchg", "missed", "avg KB")
	for _, s := range stageOrder {
		fmt.Fprintf(w, " %13s", s)
	}
	fmt.Fprintf(w, "\n%-11s %5s %6s %6s %7s", "", "", "", "", "")
	for range stageOrder {
		fmt.Fprintf(w, " %13s", "med / p95 ms")
	}
	fmt.Fprintln(w)
	for _, m := range res.Modes {
		fmt.Fprintf(w, "%-11s %5d %6d %6d %7.1f", m.Mode, m.Keys, m.Unchanged, m.Missed, float64(m.AvgBytes)/1024)
		for _, s := range stageOrder {
			fmt.Fprintf(w, " %13s", fmt.Sprintf("%.1f / %.1f", m.MedianMS[s], m.P95MS[s]))
		}
		fmt.Fprintln(w)
	}
}
