// Package bubble embeds a slate editor in a Bubble Tea v2 program.
//
// Bubble Tea draws in cells and knows nothing of the image, so the block is
// drawn in two layers. View returns blank cells, which reserve the
// rectangle. The image is written straight to the terminal whenever the
// editor's picture changes. Bubble Tea rewrites only cells that change, so
// the blank cells, and the image over them, stay put between frames.
//
// Where the host's output does not reach the terminal unchanged, as in a
// Herdr pane, Through gives the model a Painter that writes the image
// another way.
package bubble

import (
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/lawsmd/slate"
	"github.com/lawsmd/slate/link"
)

// requestCellSize asks the terminal for the pixel size of one cell.
const requestCellSize = "\x1b[16t"

// redrawDelay lets Bubble Tea flush the frame that changed the block's cells
// before the image is written over them.
const redrawDelay = 50 * time.Millisecond

// eraseDelay lets a detached painter's terminal finish repainting after a
// change of view before the remains of the image are looked for.
const eraseDelay = 150 * time.Millisecond

// settleDelay is how long after the last changed rows a detached painter
// gets the whole image, which it keeps to write again after a wipe.
const settleDelay = 250 * time.Millisecond

type (
	updateMsg struct{}
	redrawMsg int
	wipedMsg  struct{}
	againMsg  struct{}
	outerMsg  struct{}
	eraseMsg  Rect
	settleMsg int
)

// Model is the editor block. Create it with New and use it by pointer.
type Model struct {
	ed         *slate.Editor
	x, y, w, h int // the rectangle in window cells, counted from 0
	done       chan struct{}
	dragging   slate.MouseButton
	draws      int
	drawBytes  int

	paint      Painter
	reg        *link.Registration // nil when the link is off
	painted    *Rect              // where the painter last wrote the image
	winW, winH int                // the host's window in cells
	redraws    int                // counts calls to Redraw; only the newest one draws
	partials   int                // counts draws of changed rows; the newest one settles
}

// New wraps an editor. The caller still owns the editor and closes it.
func New(ed *slate.Editor) *Model {
	m := &Model{ed: ed, paint: stdout{}, done: make(chan struct{})}
	// Other programs of this user can read and edit the document through
	// package link, unless SLATE_LINK=0 is set. They cannot save or send it.
	if link.Enabled() {
		m.reg, _ = link.Register(ed, link.Info{})
	}
	return m
}

// Describe tells programs that reach the editor through package link what
// the document is, such as "comment on PROJ-123", and whatever else helps
// them choose it among several.
func (m *Model) Describe(title string, meta map[string]string) {
	if m.reg != nil {
		m.reg.Describe(link.Info{Title: title, Meta: meta})
	}
}

// Through makes the model write the image with p. Call it before the
// program starts.
func (m *Model) Through(p Painter) { m.paint = p }

// Close stops the model's waiting commands. It does not close the editor. With a
// detached painter it also removes the image, which would stay behind.
func (m *Model) Close() {
	close(m.done)
	if m.reg != nil {
		m.reg.Close()
	}
	if m.paint.Detached() && m.painted != nil {
		time.Sleep(eraseDelay)
		m.paint.Erase(*m.painted)
	}
}

// Init starts listening for frames and asks the terminal for its cell size.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.Raw(requestCellSize), m.cellSize(), m.wait(), m.Redraw()}
	if m.paint.Changes() != nil {
		cmds = append(cmds, m.waitOuter())
	}
	if m.paint.Wiped() != nil {
		cmds = append(cmds, m.waitWiped())
	}
	return tea.Batch(cmds...)
}

// cellSize asks the painter for the cell size. The answer arrives as the
// event a terminal's own answer makes.
func (m *Model) cellSize() tea.Cmd {
	return func() tea.Msg {
		if w, h, ok := m.paint.CellSize(); ok {
			return uv.CellSizeEvent{Width: w, Height: h}
		}
		return nil
	}
}

func (m *Model) waitOuter() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.paint.Changes():
			return outerMsg{}
		case <-m.done:
			return nil
		}
	}
}

func (m *Model) waitWiped() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.paint.Wiped():
			return wipedMsg{}
		case <-m.done:
			return nil
		}
	}
}

// target returns the cells the painter draws the block in and whether it
// can be drawn now.
func (m *Model) target() (Rect, bool) {
	return m.paint.Place(m.winW, m.winH, Rect{m.x, m.y, m.w, m.h})
}

// place tells the editor where the painter draws the block.
func (m *Model) place() {
	at, _ := m.target()
	m.ed.SetRect(at.X+1, at.Y+1, m.w, m.h)
}

// moved handles a change in where a detached painter draws the block or
// whether it shows.
func (m *Model) moved() tea.Cmd {
	now, visible := m.target()
	m.place()
	if m.painted != nil && (!visible || *m.painted != now) {
		// The terminal shows another view. Parts of the image remain in it.
		old := *m.painted
		m.painted = nil
		return tea.Tick(eraseDelay, func(time.Time) tea.Msg { return eraseMsg(old) })
	}
	if visible {
		return m.Redraw()
	}
	return nil
}

func (m *Model) wait() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.ed.Updates():
			return updateMsg{}
		case <-m.done:
			return nil
		}
	}
}

// SetRect places the block at window cell (x, y), counted from 0, with the
// given size. Call it from the host's layout, then show View at that place.
func (m *Model) SetRect(x, y, w, h int) tea.Cmd {
	if w < 1 || h < 1 {
		return nil
	}
	m.x, m.y, m.w, m.h = x, y, w, h
	m.place()
	return m.Redraw()
}

// Redraw repaints the whole block shortly after the current frame. Call it
// when something was drawn over the block and has gone, such as a popup.
// Calls made close together end in one repaint.
func (m *Model) Redraw() tea.Cmd {
	m.redraws++
	n := m.redraws
	return tea.Tick(redrawDelay, func(time.Time) tea.Msg { return redrawMsg(n) })
}

// Contains reports whether window cell (x, y) is inside the block.
func (m *Model) Contains(x, y int) bool {
	return x >= m.x && x < m.x+m.w && y >= m.y && y < m.y+m.h
}

// View returns the blank cells that reserve the block's rectangle.
func (m *Model) View() string {
	if m.w < 1 || m.h < 1 {
		return ""
	}
	row := strings.Repeat(" ", m.w)
	return strings.TrimSuffix(strings.Repeat(row+"\n", m.h), "\n")
}

// Update handles keys, mouse events, pastes and the model's own messages.
// Pass it every message the host does not consume itself.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case updateMsg:
		cmds := []tea.Cmd{m.draw(Rows), m.wait()}
		if m.paint.Detached() {
			m.partials++
			n := m.partials
			cmds = append(cmds, tea.Tick(settleDelay, func(time.Time) tea.Msg { return settleMsg(n) }))
		}
		return tea.Batch(cmds...)
	case settleMsg:
		// The painter's copy of the image is older than the block. A wipe
		// now would bring the old one back for a moment.
		if int(msg) == m.partials && m.painted != nil {
			return m.draw(Whole)
		}
	case redrawMsg:
		if int(msg) != m.redraws {
			return nil // a later Redraw is on its way
		}
		return m.draw(Placed)
	case wipedMsg:
		// Something repainted the block's cells, as Herdr does when its
		// window gets the keyboard focus back, and the image went with them.
		if m.painted != nil {
			return tea.Batch(m.draw(Placed), m.waitWiped())
		}
		return m.waitWiped()
	case outerMsg:
		return tea.Batch(m.moved(), m.waitOuter())
	case eraseMsg:
		m.paint.Erase(Rect(msg))
		if _, visible := m.target(); visible {
			return m.Redraw() // the erased cells may lie under the block's new place
		}
	case tea.FocusMsg:
		// The window's return to the keyboard focus is when a detached
		// image is most often lost. The painter reports a loss it can see.
		// These two draws cover one it cannot.
		if !m.paint.Detached() {
			return nil
		}
		again := func(time.Time) tea.Msg { return againMsg{} }
		return tea.Batch(tea.Tick(200*time.Millisecond, again), tea.Tick(time.Second, again))
	case againMsg:
		if m.painted != nil {
			return m.draw(Whole)
		}
	case uv.CellSizeEvent:
		m.ed.SetCellSize(msg.Width, msg.Height)
		// Text is sized to the cell so that it tracks the terminal's zoom.
		m.ed.SetTheme(slate.Theme{FontSize: msg.Height * 3 / 4})
		return m.Redraw()
	case tea.WindowSizeMsg:
		m.winW, m.winH = msg.Width, msg.Height
		if m.paint.Detached() {
			return m.moved()
		}
		// A zoom changes the cell size and reports as a resize.
		return tea.Raw(requestCellSize)
	case tea.PasteMsg:
		m.ed.Paste(msg.Content)
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.MouseClickMsg:
		if m.Contains(msg.X, msg.Y) {
			m.dragging = button(msg.Button)
			m.mouse(msg.X, msg.Y, m.dragging, slate.Press, msg.Mod)
		}
	case tea.MouseReleaseMsg:
		if m.dragging != slate.NoButton {
			m.mouse(msg.X, msg.Y, m.dragging, slate.Release, msg.Mod)
			m.dragging = slate.NoButton
		}
	case tea.MouseMotionMsg:
		if m.dragging != slate.NoButton {
			m.mouse(msg.X, msg.Y, m.dragging, slate.Motion, msg.Mod)
		}
	case tea.MouseWheelMsg:
		if m.Contains(msg.X, msg.Y) {
			switch msg.Button {
			case tea.MouseWheelUp:
				m.mouse(msg.X, msg.Y, slate.NoButton, slate.WheelUp, msg.Mod)
			case tea.MouseWheelDown:
				m.mouse(msg.X, msg.Y, slate.NoButton, slate.WheelDown, msg.Mod)
			}
		}
	}
	return nil
}

// draw writes the editor's picture: the rows that changed, or every row
// for the other kinds.
func (m *Model) draw(kind Kind) tea.Cmd {
	if kind != Rows {
		m.ed.Invalidate()
	}
	at, visible := m.target()
	if !visible {
		return nil // the editor keeps the frame until the block shows again
	}
	seq := m.ed.Draw()
	if seq == nil {
		return nil
	}
	m.draws++
	m.drawBytes += len(seq)
	m.painted = &at
	return m.paint.Draw(at, seq, kind)
}

// Stats returns how many times the image was written and the bytes sent, for
// diagnostics.
func (m *Model) Stats() (draws, bytes int) { return m.draws, m.drawBytes }

func (m *Model) mouse(x, y int, b slate.MouseButton, action slate.MouseAction, mod tea.KeyMod) {
	// A drag may leave the block; the editor gets the nearest cell.
	ev := slate.Mouse{
		Col:    min(max(x-m.x, 0), m.w-1),
		Row:    min(max(y-m.y, 0), m.h-1),
		Button: b, Action: action, Mod: mods(mod),
	}
	m.ed.Mouse(ev)
}

func button(b tea.MouseButton) slate.MouseButton {
	switch b {
	case tea.MouseLeft:
		return slate.LeftButton
	case tea.MouseMiddle:
		return slate.MiddleButton
	case tea.MouseRight:
		return slate.RightButton
	}
	return slate.NoButton
}

func mods(m tea.KeyMod) slate.Mod {
	var out slate.Mod
	if m&tea.ModShift != 0 {
		out |= slate.Shift
	}
	if m&tea.ModAlt != 0 {
		out |= slate.Alt
	}
	if m&tea.ModCtrl != 0 {
		out |= slate.Ctrl
	}
	if m&tea.ModMeta != 0 {
		out |= slate.Meta
	}
	return out
}

var keyNames = map[rune]string{
	tea.KeyEnter: "Enter", tea.KeyBackspace: "Backspace", tea.KeyTab: "Tab", tea.KeyEscape: "Escape",
	tea.KeyUp: "ArrowUp", tea.KeyDown: "ArrowDown", tea.KeyLeft: "ArrowLeft", tea.KeyRight: "ArrowRight",
	tea.KeyHome: "Home", tea.KeyEnd: "End", tea.KeyPgUp: "PageUp", tea.KeyPgDown: "PageDown",
	tea.KeyInsert: "Insert", tea.KeyDelete: "Delete", tea.KeySpace: " ",
}

// key forwards a key press. Ctrl+C and Ctrl+X copy and cut to the system
// clipboard, because the terminal cannot select text inside an image.
func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	mod := mods(k.Mod)
	if mod == slate.Ctrl && (k.Code == 'c' || k.Code == 'x') {
		result := m.ed.Selected(k.Code == 'x')
		return func() tea.Msg {
			if s := <-result; s != "" {
				return tea.SetClipboard(s)()
			}
			return nil
		}
	}

	switch key, text, ok := translate(k); {
	case text != "":
		m.ed.Paste(text)
	case ok:
		m.ed.Key(key)
	}
	return nil
}

// translate turns a Bubble Tea key press into the editor's key, or into
// text to insert when the press carries more than one character. ok is false
// for a press the editor has no use for.
func translate(k tea.KeyPressMsg) (key slate.Key, text string, ok bool) {
	key.Mod = mods(k.Mod)
	switch name, special := keyNames[k.Code]; {
	case special:
		key.Name = name
	case utf8.RuneCountInString(k.Text) > 1:
		// Composed input arrives as a string.
		return slate.Key{}, k.Text, false
	case k.Text != "":
		key.Name, key.Text = k.Text, k.Text
		key.Mod &^= slate.Shift // the shift is already in the character
	case k.Code >= 0x20 && k.Code < utf8.MaxRune && utf8.ValidRune(k.Code) && k.Code < 0xE000:
		key.Name = string(k.Code)
	default:
		return slate.Key{}, "", false
	}
	return key, "", true
}
