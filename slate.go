// Package slate is a rich-text Markdown editing block for terminal
// applications. A headless browser runs the editor; slate forwards the
// host's keys and mouse events to it and returns each changed frame as Sixel
// for the host to write into a rectangle of the terminal.
//
// One browser serves every editor of a process, each in its own page. Calls
// that change the editor return at once and reach the page in the order they
// were made. Calls that return something from the page wait for it and take
// a context.
package slate

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"strconv"
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

// ErrClosed is returned by calls on an editor after Close.
var ErrClosed = errors.New("slate: the editor is closed")

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
	// Target names where the document goes when it is done: "markdown",
	// the default, or "jira". The target sets the syntax the editor
	// accepts and draws, marks what it cannot carry, and makes the Export.
	// See web/src/targets.
	Target string
	Theme  Theme
	Render Render
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
	// then Chrome. The editors of a process share one browser, so the path
	// counts only when it starts that browser.
	BrowserPath string
}

// Event is something an editor reports through Events: Changed, Restarted or
// Failed.
type Event interface{ event() }

// Changed says the document changed.
type Changed struct {
	// Rev is the document's revision after the change. It rises by one
	// with each change.
	Rev uint64
	// Origin is who made the change: "user" for the keyboard and mouse,
	// "host" for SetMarkdown, or the name given to Edit.
	Origin string
}

// Restarted says the browser or the editor's page stopped and slate started
// it again with the same document, theme and place.
type Restarted struct{}

// Failed says the editor could not carry out a call that returns no error
// itself, such as Key or SetTheme. Op names the call. With Op "restart" the
// editor could not be started again and is of no further use.
type Failed struct {
	Op  string
	Err error
}

func (Changed) event()   {}
func (Restarted) event() {}
func (Failed) event()    {}

// page is one loaded editor page in a browser.
type page struct {
	br      *browser.Browser
	conn    *browser.Conn
	target  string
	sess    string
	ready   chan struct{} // closed when the page's script has started
	crashed chan struct{} // closed when the page's process crashed

	readyOnce, crashOnce sync.Once
}

// Editor is one editing block backed by a page of the shared browser.
type Editor struct {
	opts Options
	pg   atomic.Pointer[page]

	updates  chan struct{}
	events   chan Event
	queue    chan func() // calls for the page, carried out in order by work
	done     chan struct{}
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

	// What a new page needs to look like the old one.
	theme  map[string]string
	render Render
	target string
	focus  bool
	doc    string // the document as the page last sent it
	head   int    // the cursor's offset in doc

	watchers map[int]func(Event)
	watchID  int

	rev     uint64 // the revision of the newest change
	docRev  uint64 // the revision that doc holds
	revBase uint64 // rev when the current page was loaded; the page counts from 0
	loading bool   // the page is being loaded; its changes are not the user's
}

// New opens an editor. The first editor of a process starts the browser,
// unless Warm did so before.
func New(opts Options) (*Editor, error) {
	e := &Editor{
		opts:    opts,
		updates: make(chan struct{}, 1),
		events:  make(chan Event, 64),
		queue:   make(chan func(), 1024),
		done:    make(chan struct{}),
		col:     max(opts.Col, 1), row: max(opts.Row, 1),
		cols: opts.Cols, rows: opts.Rows,
		cellW: opts.CellWidth, cellH: opts.CellHeight,
		theme: opts.Theme.vars(), render: opts.Render, target: opts.Target, focus: true,
		doc: opts.Markdown,
	}
	if e.cols <= 0 {
		e.cols = 80
	}
	if e.rows <= 0 {
		e.rows = 24
	}
	if e.cellW <= 0 || e.cellH <= 0 {
		e.cellW, e.cellH = 10, 20
		if w, h, ok := EnvCellSize(); ok {
			e.cellW, e.cellH = w, h
		}
	}

	br, err := acquire(opts.BrowserPath)
	if err != nil {
		return nil, err
	}
	pg, err := e.open(br)
	if err != nil {
		release()
		return nil, err
	}
	e.pg.Store(pg)
	go e.work()
	go e.watch(pg)
	return e, nil
}

// open makes a page in br and loads the editor into it with the document,
// theme and focus the Editor holds.
func (e *Editor) open(br *browser.Browser) (*page, error) {
	target, sess, err := br.OpenPage()
	if err != nil {
		return nil, err
	}
	pg := &page{
		br: br, conn: br.Conn, target: target, sess: sess,
		ready: make(chan struct{}), crashed: make(chan struct{}),
	}
	if err := e.load(pg); err != nil {
		br.ClosePage(target, sess)
		return nil, err
	}
	return pg, nil
}

func (e *Editor) load(pg *page) error {
	pg.conn.Handle(pg.sess, "Runtime.bindingCalled", func(raw json.RawMessage) { e.fromPage(pg, raw) })
	pg.conn.Handle(pg.sess, "Inspector.targetCrashed", func(json.RawMessage) {
		pg.crashOnce.Do(func() { close(pg.crashed) })
	})
	go e.frames(pg, pg.conn.Subscribe(pg.sess, "Page.screencastFrame", 64))

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
		{"Inspector.enable", nil, nil},
		{"Page.enable", nil, nil},
		{"Runtime.enable", nil, nil},
		{"Runtime.addBinding", map[string]any{"name": "slateHost"}, nil},
		{"Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true}, nil},
		{"Page.getFrameTree", nil, &tree},
	}
	for _, s := range steps {
		if err := pg.conn.Call(pg.sess, s.method, s.params, s.result); err != nil {
			return err
		}
	}
	if err := e.resize(pg); err != nil {
		return err
	}
	err := pg.conn.Call(pg.sess, "Page.setDocumentContent",
		map[string]any{"frameId": tree.FrameTree.Frame.ID, "html": pageHTML}, nil)
	if err != nil {
		return err
	}
	select {
	case <-pg.ready:
	case <-pg.conn.Done():
		return browser.ErrClosed
	case <-time.After(15 * time.Second):
		return errors.New("slate: the editor page did not start")
	}

	e.mu.Lock()
	settings := map[string]any{"theme": maps.Clone(e.theme), "render": e.render}
	if e.target != "" {
		settings["target"] = e.target
	}
	doc, head, focus := e.doc, e.head, e.focus
	e.loading, e.revBase = true, e.rev
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.loading = false
		e.mu.Unlock()
	}()

	if err := e.eval(pg, nil, "slate.configure", settings); err != nil {
		return err
	}
	if doc != "" {
		if err := e.eval(pg, nil, "slate.setMarkdown", doc, head); err != nil {
			return err
		}
	}
	if err := e.eval(pg, nil, "slate.focus", focus); err != nil {
		return err
	}
	return pg.conn.Call(pg.sess, "Page.startScreencast", map[string]any{"format": "png", "everyNthFrame": 1}, nil)
}

// resize makes the page as large as the block.
func (e *Editor) resize(pg *page) error {
	e.mu.Lock()
	w, h := e.cols*e.cellW, e.rows*e.cellH
	e.mu.Unlock()
	return pg.conn.Call(pg.sess, "Emulation.setDeviceMetricsOverride",
		map[string]any{"width": w, "height": h, "deviceScaleFactor": 1, "mobile": false}, nil)
}

// fromPage handles one message from the page's script. It runs on the
// connection's reading goroutine, so a change is recorded before a later
// answer from the page reaches its caller.
func (e *Editor) fromPage(pg *page, raw json.RawMessage) {
	var call struct {
		Name    string `json:"name"`
		Payload string `json:"payload"`
	}
	var msg struct {
		Type   string  `json:"type"`
		ID     int     `json:"id"`
		Query  string  `json:"query"`
		Rev    uint64  `json:"rev"`
		Origin string  `json:"origin"`
		Head   int     `json:"head"`
		Doc    *string `json:"doc"`
	}
	if json.Unmarshal(raw, &call) != nil || json.Unmarshal([]byte(call.Payload), &msg) != nil {
		return
	}
	switch msg.Type {
	case "ready":
		pg.readyOnce.Do(func() { close(pg.ready) })
	case "change":
		e.mu.Lock()
		e.rev = e.revBase + msg.Rev
		e.head = msg.Head
		if msg.Doc != nil {
			e.doc, e.docRev = *msg.Doc, e.rev
		}
		rev, loading := e.rev, e.loading
		e.mu.Unlock()
		if !loading {
			e.modified.Store(true)
			e.emit(Changed{Rev: rev, Origin: msg.Origin})
		}
	case "mentions":
		go e.answerMentions(pg, msg.ID, msg.Query)
	}
}

func (e *Editor) answerMentions(pg *page, id int, query string) {
	users := []Mention{}
	if e.opts.Mentions != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if found, err := e.opts.Mentions(ctx, query); err == nil && found != nil {
			users = found
		}
	}
	e.eval(pg, nil, "slate.resolve", id, users)
}

// emit tells the watchers of an event and queues it for the host. A host
// that reads none loses the oldest news first.
func (e *Editor) emit(ev Event) {
	e.mu.Lock()
	fns := make([]func(Event), 0, len(e.watchers))
	for _, fn := range e.watchers {
		fns = append(fns, fn)
	}
	e.mu.Unlock()
	for _, fn := range fns {
		fn(ev)
	}
	for {
		select {
		case e.events <- ev:
			return
		default:
		}
		select {
		case <-e.events:
		default:
		}
	}
}

// Events delivers what the editor reports: Changed, Restarted and Failed. A
// host need not read it. The channel holds the newest 64 events.
func (e *Editor) Events() <-chan Event { return e.events }

// Watch calls fn with every event from now on, besides queueing it for
// Events, and returns a function that ends that. It serves a second
// observer, such as a program that follows the document from outside the
// host. fn runs on the editor's own goroutines: it must return quickly and
// must not call the editor.
func (e *Editor) Watch(fn func(Event)) (stop func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.watchers == nil {
		e.watchers = map[int]func(Event){}
	}
	e.watchID++
	id := e.watchID
	e.watchers[id] = fn
	return func() {
		e.mu.Lock()
		delete(e.watchers, id)
		e.mu.Unlock()
	}
}

// work carries out the queued calls one at a time.
func (e *Editor) work() {
	for {
		select {
		case f := <-e.queue:
			f()
		case <-e.done:
			return
		}
	}
}

// post queues a call that returns nothing to the host. A failure is reported
// as a Failed event, except when the browser has gone: then a restart is on
// its way and reports for itself.
func (e *Editor) post(op string, f func(pg *page) error) {
	run := func() {
		if err := f(e.pg.Load()); err != nil && !errors.Is(err, browser.ErrClosed) {
			e.emit(Failed{Op: op, Err: err})
		}
	}
	select {
	case e.queue <- run:
	case <-e.done:
	}
}

// ask queues a call and waits for its result.
func ask[T any](ctx context.Context, e *Editor, f func(pg *page) (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	var zero T
	ch := make(chan result, 1)
	run := func() {
		v, err := f(e.pg.Load())
		ch <- result{v, err}
	}
	select {
	case e.queue <- run:
	case <-e.done:
		return zero, ErrClosed
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	select {
	case r := <-ch:
		return r.v, r.err
	case <-e.done:
		return zero, ErrClosed
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// watch waits for the page to stop and queues its restart.
func (e *Editor) watch(pg *page) {
	select {
	case <-pg.conn.Done():
	case <-pg.crashed:
	case <-e.done:
		return
	}
	e.post("restart", func(*page) error {
		e.restart(pg)
		return nil
	})
}

// restart opens a new page in place of old, in a new browser when the old
// one has gone.
func (e *Editor) restart(old *page) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			case <-e.done:
				return
			}
		}
		var br *browser.Browser
		if br, err = replace(old.br, e.opts.BrowserPath); err != nil {
			continue
		}
		if br == old.br {
			br.ClosePage(old.target, old.sess) // the browser lives; only the page crashed
		}
		var pg *page
		if pg, err = e.open(br); err != nil {
			continue
		}
		e.pg.Store(pg)
		e.Invalidate()
		go e.watch(pg)
		e.emit(Restarted{})
		return
	}
	e.emit(Failed{Op: "restart", Err: err})
}

// eval calls a function in the page with JSON arguments and decodes its
// return value into out, which may be nil.
func (e *Editor) eval(pg *page, out any, fn string, args ...any) error {
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
	err := pg.conn.Call(pg.sess, "Runtime.evaluate", map[string]any{
		"expression":    fn + "(" + strings.Join(parts, ",") + ")",
		"returnByValue": true,
		"awaitPromise":  true,
	}, &res)
	if err != nil {
		return err
	}
	if res.Exception != nil {
		// The description is "Error: the message" and then the stack.
		msg, _, _ := strings.Cut(res.Exception.Exception.Description, "\n")
		if msg = strings.TrimPrefix(msg, "Error: "); msg == "" {
			msg = res.Exception.Text
		}
		return fmt.Errorf("%s: %s", strings.TrimPrefix(fn, "slate."), msg)
	}
	if out == nil || len(res.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(res.Result.Value, out)
}

// get queues a call of a page function and waits for what it returns.
func get[T any](ctx context.Context, e *Editor, fn string, args ...any) (T, error) {
	return ask(ctx, e, func(pg *page) (T, error) {
		var v T
		err := e.eval(pg, &v, fn, args...)
		return v, err
	})
}

// Markdown returns the document. It asks the page, after every call made
// before it has been carried out.
func (e *Editor) Markdown(ctx context.Context) (string, error) {
	s, err := get[string](ctx, e, "slate.getMarkdown")
	if errors.Is(err, browser.ErrClosed) {
		// The browser has gone. The copy kept here is the document unless
		// it was too long to be sent with its last change.
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.docRev == e.rev {
			return e.doc, nil
		}
	}
	return s, err
}

// Text returns the document as the page last reported it, with its
// revision, and asks the page nothing. A key just sent may not show in it
// yet. A document longer than 256 KB is not reported with each change, and
// Text then returns an older revision than Changed events name.
func (e *Editor) Text() (markdown string, rev uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.doc, e.docRev
}

// Snapshot is the document at one moment.
type Snapshot struct {
	Markdown string `json:"markdown"`
	Rev      uint64 `json:"rev"`
	// Selected is the text the user has selected, empty when none is.
	Selected string `json:"selected,omitempty"`
}

// Snapshot returns the document with its revision and the user's selection,
// read from the page in one step.
func (e *Editor) Snapshot(ctx context.Context) (Snapshot, error) {
	return ask(ctx, e, func(pg *page) (Snapshot, error) {
		var s Snapshot
		if err := e.eval(pg, &s, "slate.read"); err != nil {
			return s, err
		}
		e.mu.Lock()
		s.Rev += e.revBase
		e.mu.Unlock()
		return s, nil
	})
}

// Target returns the name of the target the document is written for.
func (e *Editor) Target() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.target == "" {
		return "markdown"
	}
	return e.target
}

// SetMarkdown replaces the document and puts the cursor at its start.
func (e *Editor) SetMarkdown(markdown string) {
	e.post("SetMarkdown", func(pg *page) error { return e.eval(pg, nil, "slate.setMarkdown", markdown) })
}

// Edit is one replacement of exact text.
type Edit struct {
	// Old is the text to replace. It must occur exactly once in the
	// document. Empty adds New at the end.
	Old string `json:"old"`
	New string `json:"new"`
}

// Edit replaces one piece of the document, as another writer than the user
// would: the change is one undo step, the user's cursor keeps its place in
// the text around it, and the new text is highlighted for a few seconds.
// origin names the writer in the Changed event. Edit returns the document's
// new revision, or an error when Old is absent or occurs more than once.
func (e *Editor) Edit(ctx context.Context, edit Edit, origin string) (uint64, error) {
	return ask(ctx, e, func(pg *page) (uint64, error) {
		var rev uint64
		if err := e.eval(pg, &rev, "slate.edit", edit, origin); err != nil {
			return 0, err
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.revBase + rev, nil
	})
}

// Payload is the document in the form its destination takes.
type Payload struct {
	Mime string `json:"mime"` // the media type of Body
	Body string `json:"body"`
}

// Export returns the document in the form of a target: the editor's own
// when target is empty, or the named one.
func (e *Editor) Export(ctx context.Context, target string) (Payload, error) {
	return get[Payload](ctx, e, "slate.export", target)
}

// Import replaces the document with one in the form of the editor's target,
// such as a Jira comment fetched for editing. It fails for a target that has
// no way back to Markdown.
//
// left names what the document held that Markdown has no form for and the
// editor does not show: for "jira", "media" for attached images and files.
// Exporting the edited document and sending it over the original removes
// those, so a host should warn the user first when left is not empty.
func (e *Editor) Import(ctx context.Context, body string) (left []string, err error) {
	return get[[]string](ctx, e, "slate.import", body)
}

// SetTarget changes where the document goes. The syntax the editor accepts
// and draws follows.
func (e *Editor) SetTarget(target string) {
	e.mu.Lock()
	e.target = target
	e.mu.Unlock()
	e.post("SetTarget", func(pg *page) error {
		return e.eval(pg, nil, "slate.configure", map[string]any{"target": target})
	})
}

// Tree returns the document's syntax tree as JSON, for a host that writes
// its own export:
//
//	{"markdown": "...", "root": {"type": "Document", "from": 0, "to": 12, "children": [...]}}
//
// A node's type is its name in the Markdown grammar, such as "Paragraph" or
// "Emphasis". from and to are byte offsets into markdown.
func (e *Editor) Tree(ctx context.Context) (json.RawMessage, error) {
	return get[json.RawMessage](ctx, e, "slate.tree")
}

// Loss is a stretch of the document that the target cannot carry as
// written. The editor underlines it.
type Loss struct {
	From   int    `json:"from"` // offsets in UTF-16 units, as the page counts
	To     int    `json:"to"`
	Reason string `json:"reason"` // what the export makes of it
}

// Unsupported returns what the editor's target changes or drops on export.
func (e *Editor) Unsupported(ctx context.Context) ([]Loss, error) {
	return get[[]Loss](ctx, e, "slate.unsupported")
}

// ADF returns the document as Atlassian Document Format JSON, the body of a
// Jira Cloud comment. It is Export for the "jira" target.
func (e *Editor) ADF(ctx context.Context) (json.RawMessage, error) {
	p, err := e.Export(ctx, "jira")
	return json.RawMessage(p.Body), err
}

// Modified reports whether the document changed since New or MarkSaved.
func (e *Editor) Modified() bool { return e.modified.Load() }

// MarkSaved resets Modified.
func (e *Editor) MarkSaved() { e.modified.Store(false) }

// Focus shows or hides the cursor. Without focus no Markdown syntax is
// revealed.
func (e *Editor) Focus(on bool) {
	e.mu.Lock()
	e.focus = on
	e.mu.Unlock()
	e.post("Focus", func(pg *page) error { return e.eval(pg, nil, "slate.focus", on) })
}

// SetRender changes how constructs are drawn.
func (e *Editor) SetRender(r Render) {
	e.mu.Lock()
	e.render = r
	e.mu.Unlock()
	e.post("SetRender", func(pg *page) error {
		return e.eval(pg, nil, "slate.configure", map[string]any{"render": r})
	})
}

// SetTheme changes colours and fonts. Empty fields keep what they were.
func (e *Editor) SetTheme(t Theme) {
	vars := t.vars()
	e.mu.Lock()
	maps.Copy(e.theme, vars)
	e.mu.Unlock()
	e.post("SetTheme", func(pg *page) error {
		return e.eval(pg, nil, "slate.configure", map[string]any{"theme": vars})
	})
}

// Screenshot returns the editor's current frame as a PNG.
func (e *Editor) Screenshot(ctx context.Context) ([]byte, error) {
	return ask(ctx, e, func(pg *page) ([]byte, error) {
		var res struct {
			Data []byte `json:"data"`
		}
		err := pg.conn.Call(pg.sess, "Page.captureScreenshot", map[string]any{"format": "png"}, &res)
		return res.Data, err
	})
}

// Close closes the editor's page. The browser stops with the last editor,
// unless Warm keeps it.
func (e *Editor) Close() error {
	var err error
	e.close.Do(func() {
		close(e.done)
		if pg := e.pg.Load(); pg != nil {
			pg.br.ClosePage(pg.target, pg.sess)
		}
		err = release()
	})
	return err
}

// EnvCellSize returns the cell size that the environment variable SLATE_CELL
// names, as "10x20": the pixels of one terminal cell, for a terminal or
// multiplexer that does not report them. ok is false when the variable is
// unset or is not two positive numbers.
func EnvCellSize() (w, h int, ok bool) {
	a, b, found := strings.Cut(os.Getenv("SLATE_CELL"), "x")
	if !found {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(a)
	h, errH := strconv.Atoi(b)
	if errW != nil || errH != nil || w < 1 || h < 1 {
		return 0, 0, false
	}
	return w, h, true
}
