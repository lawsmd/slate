package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/term"

	"github.com/lawsmd/slate"
	"github.com/lawsmd/slate/bubble"
	"github.com/lawsmd/slate/herdr"
)

// demoUsers stands in for a platform's user search.
var demoUsers = []slate.Mention{
	{ID: "demo-ada", Name: "Ada Lovelace"},
	{ID: "demo-alan", Name: "Alan Turing"},
	{ID: "demo-grace", Name: "Grace Hopper"},
	{ID: "demo-katherine", Name: "Katherine Johnson"},
	{ID: "demo-linus", Name: "Linus Torvalds"},
}

func demoMentions(_ context.Context, query string) ([]slate.Mention, error) {
	var out []slate.Mention
	for _, u := range demoUsers {
		if strings.Contains(strings.ToLower(u.Name), strings.ToLower(query)) {
			out = append(out, u)
		}
	}
	return out, nil
}

var helpLines = []string{
	"ctrl+s      save (notes) or submit (comment)",
	"ctrl+q      quit without saving",
	"f1          show or hide this help",
	"",
	"ctrl+b/i/e  bold, italic, inline code",
	"```         start a code block",
	"alt+l       choose a code block's language",
	"@           mention a person",
	"ctrl+c/x/v  copy, cut, paste",
	"ctrl+z/y    undo, redo",
}

var (
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("#737aa2"))
	accent = lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")).Bold(true)
	warn   = lipgloss.NewStyle().Foreground(lipgloss.Color("#e0af68"))
)

type host struct {
	ed    *slate.Editor
	block *bubble.Model

	path   string // the note's file; empty in comment mode
	crlf   bool   // the note used CRLF line endings
	w, h   int
	help   bool
	status string
	result json.RawMessage // the submitted comment
	err    error

	theme        slate.Theme // from Windows Terminal's settings; followed in a Herdr pane
	follow       bool
	cellW, cellH int    // as reported by the terminal
	background   string // as reported by the terminal
}

func notes(args []string) error {
	fs := flag.NewFlagSet("notes", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("notes needs one file")
	}
	h := &host{path: fs.Arg(0)}
	data, err := os.ReadFile(h.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The editor works in LF. The file's own line endings come back on save.
	h.crlf = bytes.Contains(data, []byte("\r\n"))
	_, err = h.run(strings.ReplaceAll(string(data), "\r\n", "\n"))
	return err
}

func comment(args []string) error {
	fs := flag.NewFlagSet("comment", flag.ExitOnError)
	out := fs.String("o", "", "file to write the ADF to; empty prints it")
	fs.Parse(args)
	h := &host{}
	result, err := h.run("")
	if err != nil || result == nil {
		return err
	}
	var pretty bytes.Buffer
	json.Indent(&pretty, result, "", "  ")
	pretty.WriteByte('\n')
	if *out != "" {
		return os.WriteFile(*out, pretty.Bytes(), 0o644)
	}
	_, err = os.Stdout.Write(pretty.Bytes())
	return err
}

// multiplexer names the terminal multiplexer this process runs under, or "".
// A multiplexer redraws its panes cell by cell and drops the image. Herdr is
// handled apart: the image goes around it.
func multiplexer() string {
	for _, m := range []struct{ env, name string }{
		{"TMUX", "tmux"}, {"ZELLIJ", "Zellij"},
	} {
		if os.Getenv(m.env) != "" {
			return m.name
		}
	}
	return ""
}

func (h *host) run(markdown string) (json.RawMessage, error) {
	if m := multiplexer(); m != "" && os.Getenv("SLATE_FORCE") == "" {
		return nil, errors.New("this shell runs inside " + m + ", which does not pass Sixel images to the terminal, " +
			"so the editor would show as an empty box.\n" +
			"Run slate in a plain Windows Terminal tab, or set SLATE_FORCE=1 to start anyway.")
	}
	var pane *herdr.Pane
	if herdr.Inside() {
		var err error
		if pane, err = herdr.Open(); err != nil {
			return nil, fmt.Errorf("this shell runs inside Herdr, and slate could not reach the terminal behind it: %w", err)
		}
		defer pane.Close()
	}
	// The terminal has no escape sequence that reports its font, and a Herdr
	// pane answers no colour query, so both start from the settings file.
	// Colours the terminal does report replace these.
	theme, _ := slate.WindowsTerminalTheme()
	h.theme = theme
	if pane == nil {
		theme.Background, theme.Foreground, theme.Selection = "", "", ""
	}
	target := "markdown"
	if h.path == "" {
		target = "jira" // a comment is written for Jira
	}
	ed, err := slate.New(slate.Options{Markdown: markdown, Target: target, Mentions: demoMentions, Theme: theme})
	if err != nil {
		return nil, err
	}
	defer ed.Close()
	h.ed, h.block = ed, bubble.New(ed)
	defer h.block.Close()
	if h.path != "" {
		abs, _ := filepath.Abs(h.path)
		h.block.Describe("note "+filepath.Base(h.path), map[string]string{"file": abs})
	} else {
		h.block.Describe("new comment", nil)
	}
	if pane != nil {
		h.block.Through(bubble.Herdr(pane))
		h.follow = true
	}

	var opts []tea.ProgramOption
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		// Standard output is redirected to take the ADF, so draw on stderr.
		opts = append(opts, tea.WithOutput(os.Stderr))
	}
	p := tea.NewProgram(h, opts...)
	if script := os.Getenv("SLATE_KEYS"); script != "" {
		go h.selfTest(p, script)
	}
	if _, err := p.Run(); err != nil {
		return nil, err
	}
	return h.result, h.err
}

// eventMsg is something the editor reported.
type eventMsg struct{ slate.Event }

func (h *host) waitEvent() tea.Cmd {
	return func() tea.Msg { return eventMsg{<-h.ed.Events()} }
}

// themeMsg is the theme read again from Windows Terminal's settings.
type themeMsg slate.Theme

// watchTheme reads the settings again after a pause. A change of colour
// scheme in Windows Terminal then reaches the editor in a Herdr pane.
func watchTheme() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		t, _ := slate.WindowsTerminalTheme()
		return themeMsg(t)
	})
}

func (h *host) Init() tea.Cmd {
	cmds := []tea.Cmd{h.block.Init(), h.waitEvent(), tea.RequestBackgroundColor, tea.RequestForegroundColor}
	if h.follow {
		cmds = append(cmds, watchTheme())
	}
	return tea.Batch(cmds...)
}

func (h *host) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h.w, h.h = msg.Width, msg.Height
		// Row 0 is the title, the last row the status line, and a border
		// surrounds the block.
		return h, tea.Batch(h.block.SetRect(1, 2, h.w-2, h.h-4), h.block.Update(msg))
	case eventMsg:
		switch ev := msg.Event.(type) {
		case slate.Restarted:
			h.status = warn.Render("the editor's browser stopped and was started again")
		case slate.Failed:
			h.status = warn.Render(ev.Op + " failed: " + ev.Err.Error())
		}
		return h, h.waitEvent()
	case themeMsg:
		if t := slate.Theme(msg); t.Background != h.theme.Background || t.Foreground != h.theme.Foreground ||
			t.Selection != h.theme.Selection || t.MonoFont != h.theme.MonoFont {
			h.theme = t
			h.ed.SetTheme(t)
		}
		return h, watchTheme()
	case uv.CellSizeEvent:
		h.cellW, h.cellH = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		h.background = msg.String()
		h.ed.SetTheme(slate.Theme{Background: msg.String()})
		return h, nil
	case tea.ForegroundColorMsg:
		h.ed.SetTheme(slate.Theme{Foreground: msg.String()})
		return h, nil
	case tea.KeyPressMsg:
		// SLATE_KEYLOG names a file that gets each key press as the terminal
		// reported it, for working out why a key does nothing.
		if path := os.Getenv("SLATE_KEYLOG"); path != "" {
			if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintf(f, "%s: %+v\n", msg.String(), tea.Key(msg))
				f.Close()
			}
		}
		switch msg.String() {
		case "ctrl+q":
			return h, tea.Quit
		case "ctrl+s":
			return h.save()
		case "f1":
			h.help = !h.help
			h.ed.Focus(!h.help)
			// The block draws no image while the popup is over it, and
			// the whole image when the popup has gone.
			return h, h.block.Cover(h.help)
		}
		if h.help {
			return h, nil
		}
	}
	return h, h.block.Update(msg)
}

func (h *host) save() (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if h.path == "" {
		h.result, h.err = h.ed.ADF(ctx)
		return h, tea.Quit
	}
	md, err := h.ed.Markdown(ctx)
	if err == nil {
		if h.crlf {
			md = strings.ReplaceAll(md, "\n", "\r\n")
		}
		err = os.WriteFile(h.path, []byte(md), 0o644)
	}
	if err != nil {
		h.status = warn.Render("not saved: " + err.Error())
		return h, nil
	}
	h.ed.MarkSaved()
	h.status = "saved " + filepath.Base(h.path)
	return h, nil
}

func (h *host) View() tea.View {
	v := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion, ReportFocus: true}
	if h.w < 20 || h.h < 8 {
		v.Content = "window too small"
		return v
	}
	inner := h.w - 2
	rows := h.h - 4

	title := "new comment"
	if h.path != "" {
		title = filepath.Base(h.path)
	}
	modified := ""
	if h.ed.Modified() {
		modified = warn.Render(" (modified)")
	}

	// The popup is drawn over the block's blank cells.
	popup := map[int]string{}
	if h.help {
		width := 0
		for _, l := range helpLines {
			width = max(width, len(l))
		}
		top := max((rows-len(helpLines)-2)/2, 0)
		left := max((inner-width-4)/2, 0)
		box := func(s string) string { return strings.Repeat(" ", left) + s }
		popup[top] = box(dim.Render("╭" + strings.Repeat("─", width+2) + "╮"))
		for i, l := range helpLines {
			popup[top+1+i] = box(dim.Render("│") + " " + l + strings.Repeat(" ", width-len(l)) + " " + dim.Render("│"))
		}
		popup[top+1+len(helpLines)] = box(dim.Render("╰" + strings.Repeat("─", width+2) + "╯"))
	}

	var b strings.Builder
	b.WriteString(" " + accent.Render(title) + modified + "\n")
	b.WriteString(dim.Render("╭"+strings.Repeat("─", inner)+"╮") + "\n")
	blank := strings.Repeat(" ", inner)
	for r := 0; r < rows; r++ {
		content := blank
		if p, ok := popup[r]; ok {
			content = p + strings.Repeat(" ", max(inner-lipgloss.Width(p), 0))
		}
		b.WriteString(dim.Render("│") + content + dim.Render("│") + "\n")
	}
	b.WriteString(dim.Render("╰"+strings.Repeat("─", inner)+"╯") + "\n")
	status := h.status
	if status == "" {
		status = "ctrl+s " + map[bool]string{true: "save", false: "submit"}[h.path != ""] + "   ctrl+q quit   f1 help"
	}
	b.WriteString(" " + dim.Render(status))
	v.Content = b.String()
	return v
}

// selfTest types the characters of SLATE_KEYS, saves, and quits. With
// SLATE_TRACE set it also writes what the terminal reported and how often
// the image was drawn. It checks the host's plumbing in a real terminal
// without a person at the keyboard.
func (h *host) selfTest(p *tea.Program, script string) {
	time.Sleep(1500 * time.Millisecond)
	// A person clicks into the block before typing.
	p.Send(tea.MouseClickMsg{X: 10, Y: 5, Button: tea.MouseLeft})
	time.Sleep(50 * time.Millisecond)
	p.Send(tea.MouseReleaseMsg{X: 10, Y: 5, Button: tea.MouseLeft})
	time.Sleep(200 * time.Millisecond)
	for _, r := range script {
		if r == '\n' {
			p.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
		} else {
			p.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	p.Send(tea.KeyPressMsg{Code: tea.KeyF1})
	time.Sleep(300 * time.Millisecond)
	p.Send(tea.KeyPressMsg{Code: tea.KeyF1})
	time.Sleep(300 * time.Millisecond)
	if path := os.Getenv("SLATE_TRACE"); path != "" {
		draws, drawBytes := h.block.Stats()
		trace, _ := json.MarshalIndent(map[string]any{
			"window": []int{h.w, h.h}, "cell": []int{h.cellW, h.cellH},
			"background": h.background, "draws": draws, "drawBytes": drawBytes,
		}, "", "  ")
		os.WriteFile(path, trace, 0o644)
	}
	p.Send(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	time.Sleep(300 * time.Millisecond)
	p.Send(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
}
