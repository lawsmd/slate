// Package bubble embeds a slate editor in a Bubble Tea v2 program.
//
// Bubble Tea draws in cells and knows nothing of the image, so the block is
// drawn in two layers. View returns blank cells, which reserve the
// rectangle. The image is written straight to the terminal whenever the
// editor's picture changes. Bubble Tea rewrites only cells that change, so
// the blank cells, and the image over them, stay put between frames.
//
// In a Herdr pane the image cannot go through the pane. Through sends it to
// the terminal Herdr runs in, at the pane's place there.
package bubble

import (
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/lawsmd/slate"
	"github.com/lawsmd/slate/herdr"
)

// requestCellSize asks the terminal for the pixel size of one cell.
const requestCellSize = "\x1b[16t"

// redrawDelay lets Bubble Tea flush the frame that changed the block's cells
// before the image is written over them.
const redrawDelay = 50 * time.Millisecond

// eraseDelay lets Herdr finish repainting after a change of view before the
// remains of the image are looked for.
const eraseDelay = 150 * time.Millisecond

type (
	updateMsg struct{}
	redrawMsg int
	wipedMsg  struct{}
	againMsg  struct{}
	outerMsg  struct{}
	eraseMsg  cells
)

// cells is a rectangle of the outer terminal, counted from 0.
type cells struct{ x, y, w, h int }

// Model is the editor block. Create it with New and use it by pointer.
type Model struct {
	ed         *slate.Editor
	x, y, w, h int // the rectangle in window cells, counted from 0
	queue      chan func()
	done       chan struct{}
	dragging   slate.MouseButton
	draws      int
	drawBytes  int

	outer      *herdr.Pane // set when the image goes around Herdr
	painted    *cells      // where the image was last written in the outer terminal
	winW, winH int         // the host's window in cells: in Herdr, the inside of the pane
	redraws    int         // counts calls to Redraw; only the newest one draws
}

// New wraps an editor. The caller still owns the editor and closes it.
func New(ed *slate.Editor) *Model {
	m := &Model{ed: ed, queue: make(chan func(), 1024), done: make(chan struct{})}
	// Input is sent from one goroutine so that keys reach the editor in the
	// order they were typed without holding up Bubble Tea's update loop.
	go func() {
		for {
			select {
			case f := <-m.queue:
				f()
			case <-m.done:
				return
			}
		}
	}()
	return m
}

// Through makes the model write the image to the terminal that Herdr runs
// in. Call it before the program starts. The caller closes the pane, after
// closing the model.
func (m *Model) Through(p *herdr.Pane) { m.outer = p }

// Close stops the model's goroutine. It does not close the editor. In a
// Herdr pane it also removes the image, which Herdr would leave behind.
func (m *Model) Close() {
	close(m.done)
	if m.outer != nil && m.painted != nil {
		time.Sleep(eraseDelay)
		m.outer.EraseBlank(m.painted.x, m.painted.y, m.painted.w, m.painted.h)
	}
}

// Init starts listening for frames and asks the terminal for its cell size.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.Raw(requestCellSize), m.wait(), m.Redraw()}
	if m.outer != nil {
		cmds = append(cmds, m.waitOuter(), m.waitWiped())
	}
	return tea.Batch(cmds...)
}

func (m *Model) waitOuter() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.outer.Changes():
			return outerMsg{}
		case <-m.done:
			return nil
		}
	}
}

func (m *Model) waitWiped() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.outer.Wiped():
			return wipedMsg{}
		case <-m.done:
			return nil
		}
	}
}

// target returns the block's cells in the outer terminal and whether the
// pane shows.
func (m *Model) target() (cells, bool) {
	st := m.outer.State()
	x, y := st.X, st.Y
	// Herdr's rectangle includes the border it draws round a pane that
	// shares its tab. The host's window is the inside.
	bw, bh := st.W-m.winW, st.H-m.winH
	if m.winW == 0 || (bw != 0 && bw != 2) || (bh != 0 && bh != 2) {
		// The pane was resized and one of the two sizes is not yet known.
		// Drawing now could put the image over another pane.
		return cells{}, false
	}
	return cells{x + bw/2 + m.x, y + bh/2 + m.y, m.w, m.h}, st.Visible
}

// place tells the editor where the block is. In a Herdr pane that is the
// pane's place in the outer terminal plus the block's place in the pane.
func (m *Model) place() {
	at := cells{m.x, m.y, m.w, m.h}
	if m.outer != nil {
		at, _ = m.target()
	}
	m.ed.SetRect(at.x+1, at.y+1, m.w, m.h)
}

// moved handles a change in where the Herdr pane is or whether it shows.
func (m *Model) moved() tea.Cmd {
	now, visible := m.target()
	m.place()
	if m.painted != nil && (!visible || *m.painted != now) {
		// Herdr has drawn another view. Parts of the image remain in it.
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
		return tea.Batch(m.draw(false, false), m.wait())
	case redrawMsg:
		if int(msg) != m.redraws {
			return nil // a later Redraw is on its way
		}
		return m.draw(true, true)
	case wipedMsg:
		// Herdr repainted the block's cells, as it does when its window
		// gets the keyboard focus back, and the image went with them.
		if m.painted != nil {
			return tea.Batch(m.draw(true, true), m.waitWiped())
		}
		return m.waitWiped()
	case outerMsg:
		return tea.Batch(m.moved(), m.waitOuter())
	case eraseMsg:
		m.outer.EraseBlank(msg.x, msg.y, msg.w, msg.h)
		if m.outer.State().Visible {
			return m.Redraw() // the erased cells may lie under the block's new place
		}
	case tea.FocusMsg:
		// In Herdr the window's return to the keyboard focus is when the
		// image is most often lost. The helper sees a loss that Herdr's
		// repaint causes. These two draws cover one it cannot see.
		if m.outer == nil {
			return nil
		}
		again := func(time.Time) tea.Msg { return againMsg{} }
		return tea.Batch(tea.Tick(200*time.Millisecond, again), tea.Tick(time.Second, again))
	case againMsg:
		if m.painted != nil {
			return m.draw(false, true)
		}
	case uv.CellSizeEvent:
		m.ed.SetCellSize(msg.Width, msg.Height)
		// Text is sized to the cell so that it tracks the terminal's zoom.
		m.ed.SetTheme(slate.Theme{FontSize: msg.Height * 3 / 4})
		return m.Redraw()
	case tea.WindowSizeMsg:
		m.winW, m.winH = msg.Width, msg.Height
		if m.outer != nil {
			return m.moved()
		}
		// A zoom changes the cell size and reports as a resize.
		return tea.Raw(requestCellSize)
	case tea.PasteMsg:
		m.queue <- func() { m.ed.Paste(msg.Content) }
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
// when whole is set. In a Herdr pane, mark also marks the block's cells
// again, which a draw after a move or a wipe needs. A whole draw without
// marks writes the image over itself and never shows the block empty.
func (m *Model) draw(mark, whole bool) tea.Cmd {
	if whole {
		m.ed.Invalidate()
	}
	var at cells
	if m.outer != nil {
		var visible bool
		if at, visible = m.target(); !visible {
			return nil // the editor keeps the frame until the pane shows again
		}
	}
	seq := m.ed.Draw()
	if seq == nil {
		return nil
	}
	m.draws++
	m.drawBytes += len(seq)
	if m.outer != nil {
		if mark {
			m.outer.Draw(at.x, at.y, at.w, at.h, seq)
		} else {
			m.outer.Write(seq)
		}
		m.painted = &at
		return nil
	}
	return tea.Raw(string(seq))
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
	m.queue <- func() { m.ed.Mouse(ev) }
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
		result := make(chan string, 1)
		m.queue <- func() {
			var s string
			if k.Code == 'x' {
				s, _ = m.ed.Cut()
			} else {
				s, _ = m.ed.Copy()
			}
			result <- s
		}
		return func() tea.Msg {
			if s := <-result; s != "" {
				return tea.SetClipboard(s)()
			}
			return nil
		}
	}

	key := slate.Key{Mod: mod}
	switch name, special := keyNames[k.Code]; {
	case special:
		key.Name = name
	case utf8.RuneCountInString(k.Text) > 1:
		// Composed input arrives as a string.
		m.queue <- func() { m.ed.Paste(k.Text) }
		return nil
	case k.Text != "":
		key.Name, key.Text = k.Text, k.Text
		key.Mod &^= slate.Shift // the shift is already in the character
	case k.Code >= 0x20 && k.Code < utf8.MaxRune && utf8.ValidRune(k.Code) && k.Code < 0xE000:
		key.Name = string(k.Code)
	default:
		return nil
	}
	m.queue <- func() { m.ed.Key(key) }
	return nil
}
