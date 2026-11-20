// Command embed is the smallest host for a slate block: a Bubble Tea program
// with a comment box in its right-hand panel. It shows what the host decides
// (where the block is, which keys it keeps, when the text is saved or sent)
// and what slate does (everything inside the rectangle).
//
//	go run ./examples/embed
//
// tab moves the keyboard between the list and the box. ctrl+s "sends" the
// comment, which here prints its ADF after the program ends. esc quits.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lawsmd/slate"
	"github.com/lawsmd/slate/bubble"
)

type model struct {
	block   *bubble.Model
	w, h    int
	writing bool   // the keyboard is in the box
	draft   string // the text as of the last change, as a host would keep a draft
	status  string
	sent    string
}

// changed is an event from the editor.
type changed struct{ slate.Event }

func (m *model) events() tea.Cmd {
	return func() tea.Msg { return changed{<-m.block.Editor().Events()} }
}

func (m *model) Init() tea.Cmd { return tea.Batch(m.block.Init(), m.events()) }

// layout puts the block in the right-hand panel, inside its border.
func (m *model) layout() tea.Cmd {
	left := m.w / 3
	return m.block.SetRect(left+1, 2, m.w-left-2, m.h-4)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		// The block also needs the message, to know the window's size.
		return m, tea.Batch(m.layout(), m.block.Update(msg))
	case changed:
		switch ev := msg.Event.(type) {
		case slate.Changed:
			// The host decides what a draft is. This one keeps the text.
			m.draft, _ = m.block.Editor().Text()
			m.status = fmt.Sprintf("draft kept at revision %d, written by %s", ev.Rev, ev.Origin)
		case slate.Failed:
			m.status = ev.Op + " failed: " + ev.Err.Error()
		}
		return m, m.events()
	case tea.KeyPressMsg:
		// Keys the host keeps for itself, wherever the keyboard is.
		switch msg.String() {
		case "esc":
			return m, tea.Quit
		case "tab":
			m.writing = !m.writing
			m.block.Editor().Focus(m.writing) // shows or hides the cursor
			return m, nil
		case "ctrl+s":
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := m.block.Editor().Export(ctx, "jira")
			if err != nil {
				m.status = "not sent: " + err.Error()
				return m, nil
			}
			m.sent = out.Body // a real host posts this to Jira
			return m, tea.Quit
		}
		if !m.writing {
			return m, nil // the list would handle its own keys here
		}
	}
	// Everything else is the block's: keys while writing, the mouse, pastes,
	// and its own messages.
	return m, m.block.Update(msg)
}

func (m *model) View() tea.View {
	v := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion, ReportFocus: true}
	if m.w < 40 || m.h < 8 {
		v.Content = "window too small"
		return v
	}
	left := m.w / 3
	right := m.w - left - 2
	rows := m.h - 4
	list := []string{"PROJ-101  Login fails", "PROJ-102  Slow export", "PROJ-103  Typo in footer"}
	// The block's cells are blank: View returns rows of spaces, and slate
	// draws the image over them.
	blank := strings.Split(m.block.View(), "\n")

	var b strings.Builder
	b.WriteString(pad(" Tickets", left) + " Comment on PROJ-101\n")
	b.WriteString(strings.Repeat(" ", left) + "╭" + strings.Repeat("─", right) + "╮\n")
	for r := 0; r < rows; r++ {
		item := ""
		if r < len(list) {
			item = " " + list[r]
		}
		b.WriteString(pad(item, left) + "│" + blank[r] + "│\n")
	}
	b.WriteString(strings.Repeat(" ", left) + "╰" + strings.Repeat("─", right) + "╯\n")
	where := "list"
	if m.writing {
		where = "comment box"
	}
	b.WriteString(pad(" keyboard: "+where+"   tab switch   ctrl+s send   esc quit   "+m.status, m.w))
	v.Content = b.String()
	return v
}

func pad(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		return string(r[:w])
	}
	return s + strings.Repeat(" ", w-len(r))
}

func main() {
	slate.Warm() // start the browser while the program sets up
	defer slate.Shutdown()

	block, err := bubble.Open(bubble.Config{
		Options: slate.Options{Target: "jira", Markdown: "Type a **comment** here."},
		Title:   "comment on PROJ-101",
		Meta:    map[string]string{"ticket": "PROJ-101"},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	block.Editor().Focus(false) // the keyboard starts in the list
	m := &model{block: block}
	_, err = tea.NewProgram(m).Run()
	block.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if m.sent != "" {
		fmt.Println(m.sent)
	}
}
