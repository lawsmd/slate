// Package slate is a rich-text Markdown editing block for terminal
// applications. A headless browser runs the editor; slate forwards the
// host's keys and mouse events to it and returns each changed frame as Sixel
// for the host to write into a rectangle of the terminal.
package slate

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lawsmd/slate/browser"
	"github.com/lawsmd/slate/sixel"
)

//go:generate npm --prefix web run build

//go:embed web/dist/index.html
var pageHTML string

// Style is how one Markdown construct is drawn.
type Style string

const (
	// Live hides syntax characters until the cursor reaches them, as
	// Obsidian's live preview does.
	Live Style = "live"
	// Block draws the construct as a box or widget, as Jira's editor does.
	// Its syntax shows only while the cursor is on the syntax line itself.
	// Code blocks and tables have a block renderer; other constructs are
	// drawn as Live.
	Block Style = "block"
	// Raw shows the Markdown text as typed.
	Raw Style = "raw"
)

// Render chooses a Style per construct. An empty field keeps the default:
// Block for Code and Table, Live for the others.
type Render struct {
	Heading  Style `json:"heading,omitempty"`
	Emphasis Style `json:"emphasis,omitempty"`
	Link     Style `json:"link,omitempty"`
	List     Style `json:"list,omitempty"`
	Quote    Style `json:"quote,omitempty"`
	Callout  Style `json:"callout,omitempty"`
	Code     Style `json:"code,omitempty"`
	Table    Style `json:"table,omitempty"`
	Rule     Style `json:"rule,omitempty"`
	Mention  Style `json:"mention,omitempty"`
}

// Mention is one user offered when "@" is typed.
type Mention struct {
	ID   string `json:"id"`   // the platform's account ID
	Name string `json:"name"` // the display name
}

// Options configures New. The zero value is an 80 by 24 cell block with the
// default theme.
type Options struct {
	// Markdown is the initial document.
	Markdown string
	Theme    Theme
	Render   Render
	// Mentions searches users for the text typed after "@". Nil turns
	// mention completion off.
	Mentions func(ctx context.Context, query string) ([]Mention, error)

	// Col and Row are the terminal cell of the block's top-left corner,
	// counted from 1. Cols and Rows are its size in cells.
	Col, Row, Cols, Rows int
	// CellWidth and CellHeight are the pixel size of one terminal cell.
	// The default is 10 by 20.
	CellWidth, CellHeight int

	// BrowserPath is the browser executable. Empty means look for Edge,
	// then Chrome.
	BrowserPath string
}

// Editor is one editing block backed by a headless browser page.
type Editor struct {
	br   *browser.Browser
	conn *browser.Conn
	sess string
	opts Options

	updates  chan struct{}
	done     chan struct{}
	ready    chan struct{}
	modified atomic.Bool
	close    sync.Once

	mu           sync.Mutex // guards the fields below
	col, row     int
	cols, rows   int
	cellW, cellH int
	frame        frame  // the newest frame from the browser
	shown        []byte // the pixels of the frame last drawn
	enc          sixel.Encoder
	click        clickState
}

// New starts a browser and loads the editor.
func New(opts Options) (*Editor, error) {
	e := &Editor{
		opts:    opts,
		updates: make(chan struct{}, 1),
		done:    make(chan struct{}),
		ready:   make(chan struct{}),
		col:     max(opts.Col, 1), row: max(opts.Row, 1),
		cols: opts.Cols, rows: opts.Rows,
		cellW: opts.CellWidth, cellH: opts.CellHeight,
	}
	if e.cols <= 0 {
		e.cols = 80
	}
	if e.rows <= 0 {
		e.rows = 24
	}
	if e.cellW <= 0 || e.cellH <= 0 {
		e.cellW, e.cellH = 10, 20
	}

	br, err := browser.Launch(browser.Options{Path: opts.BrowserPath})
	if err != nil {
		return nil, err
	}
	e.br, e.conn = br, br.Conn
	if err := e.load(); err != nil {
		br.Close()
		return nil, err
	}
	return e, nil
}

func (e *Editor) load() error {
	var err error
	if e.sess, err = e.br.NewPage(); err != nil {
		return err
	}
	bindings := e.conn.Subscribe(e.sess, "Runtime.bindingCalled", 256)
	frames := e.conn.Subscribe(e.sess, "Page.screencastFrame", 64)
	go e.listen(bindings, frames)

	var tree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	steps := []struct {
		method string
		params any
		result any
	}{
		{"Page.enable", nil, nil},
		{"Runtime.enable", nil, nil},
		{"Runtime.addBinding", map[string]any{"name": "slateHost"}, nil},
		{"Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true}, nil},
		{"Page.getFrameTree", nil, &tree},
	}
	for _, s := range steps {
		if err := e.conn.Call(e.sess, s.method, s.params, s.result); err != nil {
			return err
		}
	}
	if err := e.resize(); err != nil {
		return err
	}
	err = e.conn.Call(e.sess, "Page.setDocumentContent",
		map[string]any{"frameId": tree.FrameTree.Frame.ID, "html": pageHTML}, nil)
	if err != nil {
		return err
	}
	select {
	case <-e.ready:
	case <-time.After(15 * time.Second):
		return errors.New("slate: the editor page did not start")
	}

	settings := map[string]any{"theme": e.opts.Theme.vars(), "render": e.opts.Render}
	if err := e.eval(nil, "slate.configure", settings); err != nil {
		return err
	}
	if e.opts.Markdown != "" {
		if err := e.SetMarkdown(e.opts.Markdown); err != nil {
			return err
		}
	}
	if err := e.Focus(true); err != nil {
		return err
	}
	e.modified.Store(false)
	return e.conn.Call(e.sess, "Page.startScreencast", map[string]any{"format": "png", "everyNthFrame": 1}, nil)
}

// resize makes the page as large as the block.
func (e *Editor) resize() error {
	e.mu.Lock()
	w, h := e.cols*e.cellW, e.rows*e.cellH
	e.mu.Unlock()
	return e.conn.Call(e.sess, "Emulation.setDeviceMetricsOverride",
		map[string]any{"width": w, "height": h, "deviceScaleFactor": 1, "mobile": false}, nil)
}

// listen handles messages from the page and frames from the screencast.
func (e *Editor) listen(bindings, frames <-chan json.RawMessage) {
	var readyOnce sync.Once
	for {
		select {
		case <-e.done:
			return
		case raw := <-bindings:
			var call struct {
				Name    string `json:"name"`
				Payload string `json:"payload"`
			}
			var msg struct {
				Type  string `json:"type"`
				ID    int    `json:"id"`
				Query string `json:"query"`
			}
			if json.Unmarshal(raw, &call) != nil || json.Unmarshal([]byte(call.Payload), &msg) != nil {
				continue
			}
			switch msg.Type {
			case "ready":
				readyOnce.Do(func() { close(e.ready) })
			case "change":
				e.modified.Store(true)
			case "mentions":
				go e.answerMentions(msg.ID, msg.Query)
			}
		case raw := <-frames:
			e.receive(raw)
		}
	}
}

func (e *Editor) answerMentions(id int, query string) {
	users := []Mention{}
	if e.opts.Mentions != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if found, err := e.opts.Mentions(ctx, query); err == nil && found != nil {
			users = found
		}
	}
	e.eval(nil, "slate.resolve", id, users)
}

// eval calls a function in the page with JSON arguments and decodes its
// return value into out, which may be nil.
func (e *Editor) eval(out any, fn string, args ...any) error {
	parts := make([]string, len(args))
	for i, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			return err
		}
		parts[i] = string(b)
	}
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	err := e.conn.Call(e.sess, "Runtime.evaluate", map[string]any{
		"expression":    fn + "(" + strings.Join(parts, ",") + ")",
		"returnByValue": true,
		"awaitPromise":  true,
	}, &res)
	if err != nil {
		return err
	}
	if res.Exception != nil {
		return fmt.Errorf("slate: %s: %s %s", fn, res.Exception.Text, res.Exception.Exception.Description)
	}
	if out == nil || len(res.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(res.Result.Value, out)
}

// Markdown returns the document.
func (e *Editor) Markdown() (string, error) {
	var s string
	err := e.eval(&s, "slate.getMarkdown")
	return s, err
}

// SetMarkdown replaces the document and puts the cursor at its start.
func (e *Editor) SetMarkdown(markdown string) error {
	return e.eval(nil, "slate.setMarkdown", markdown)
}

// ADF returns the document as Atlassian Document Format JSON, the body of a
// Jira Cloud comment.
func (e *Editor) ADF() (json.RawMessage, error) {
	var doc json.RawMessage
	err := e.eval(&doc, "slate.getADF")
	return doc, err
}

// Modified reports whether the document changed since New or MarkSaved.
func (e *Editor) Modified() bool { return e.modified.Load() }

// MarkSaved resets Modified.
func (e *Editor) MarkSaved() { e.modified.Store(false) }

// Focus shows or hides the cursor. Without focus no Markdown syntax is
// revealed.
func (e *Editor) Focus(on bool) error { return e.eval(nil, "slate.focus", on) }

// SetRender changes how constructs are drawn.
func (e *Editor) SetRender(r Render) error {
	return e.eval(nil, "slate.configure", map[string]any{"render": r})
}

// SetTheme changes colours and fonts.
func (e *Editor) SetTheme(t Theme) error {
	return e.eval(nil, "slate.configure", map[string]any{"theme": t.vars()})
}

// Screenshot returns the editor's current frame as a PNG.
func (e *Editor) Screenshot() ([]byte, error) {
	var res struct {
		Data []byte `json:"data"`
	}
	err := e.conn.Call(e.sess, "Page.captureScreenshot", map[string]any{"format": "png"}, &res)
	return res.Data, err
}

// Close stops the browser.
func (e *Editor) Close() error {
	var err error
	e.close.Do(func() {
		close(e.done)
		err = e.br.Close()
	})
	return err
}
