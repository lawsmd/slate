# Embedding slate in a Bubble Tea program

This is the guide for putting a slate editor inside another terminal application. It
assumes a Go program on Bubble Tea v2 (`charm.land/bubbletea/v2`). `examples/embed/main.go`
is a complete host in about 150 lines and follows this guide step by step.

## Division of work

slate gives the host a rectangle of cells and handles everything inside it: rendering,
the cursor, selection, typing, mouse clicks, copy and paste, undo, mention completion.

The host decides everything else:

- where the rectangle is and how big, and when that changes;
- which keys and mouse events the editor gets, and which the host keeps;
- any title, border, status line or help around the rectangle;
- when the text is read, saved as a draft, or sent, and in which format;
- whether other programs may read and edit the text through the link.

slate never saves or sends a document.

## Requirements

- Go, Bubble Tea v2, and `go get github.com/lawsmd/slate`. The editor page is embedded in
  the module. No Node.js is needed to use slate.
- Microsoft Edge or Chrome on the machine. slate starts it headless.
- A terminal that shows Sixel images: Windows Terminal 1.22 or later. Inside Herdr slate
  draws around Herdr, and `bubble.Open` sets that up.

## Opening a block

```go
slate.Warm()            // optional, at program start: starts the browser in the background
defer slate.Shutdown()  // with Warm: stops the browser at exit

block, err := bubble.Open(bubble.Config{
    Options: slate.Options{
        Target:   "jira",        // or "markdown"
        Markdown: draft,         // the initial text
        Mentions: searchUsers,   // optional: func(ctx, query) ([]slate.Mention, error)
    },
    Title: "comment on PROJ-123",                    // shown by `slate ls`
    Meta:  map[string]string{"ticket": "PROJ-123"},
})
defer block.Close()     // closes the editor too
ed := block.Editor()
```

`bubble.Open` detects Herdr, takes the terminal's colours and code font, and returns an
error under tmux or Zellij, which drop the image. A host needs `slate.New`, `bubble.New`
and `Through` only to do one of those differently.

Without `Warm`, the first block takes about 0.8 s to open and later ones about 0.3 s.
With `Warm` at startup, a block opened a second later appears at once.

## Wiring the model

A block is not a `tea.Model`. The host calls four of its methods from its own model.

```go
func (m *model) Init() tea.Cmd { return m.block.Init() }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        // Place the block, and pass the message on: the block needs the window's size.
        return m, tea.Batch(m.block.SetRect(x, y, w, h), m.block.Update(msg))
    case tea.KeyPressMsg:
        if hostHandles(msg) {
            return m, nil
        }
    }
    return m, m.block.Update(msg) // everything the host does not consume
}
```

- `SetRect(x, y, w, h)` takes window cells counted from 0. Call it whenever the layout
  changes. It returns a command; return it from `Update`.
- `View()` returns `h` rows of `w` spaces. Put them at the rectangle's place in the host's
  view. The host must not draw anything else in those cells: text written over a cell
  erases the image there.
- `Update(msg)` must receive every message the host does not consume. That includes
  messages the host does not know: the block's own messages arrive through the host's
  `Update`. A host that drops unknown messages shows a frozen or empty block.
- The host's `tea.View` should set `MouseMode: tea.MouseModeCellMotion` for clicks and
  drags in the block, and `ReportFocus: true`.

## Keyboard focus

The editor types whatever keys the host passes to `Update`. To move the keyboard away
from the block, stop passing key presses and call `ed.Focus(false)`, which hides the
cursor. `ed.Focus(true)` shows it again.

The block uses these keys when it receives them: `ctrl+c` and `ctrl+x` (copy and cut to
the system clipboard), `ctrl+z` and `ctrl+y`, `ctrl+b`, `ctrl+i` and `ctrl+e` (bold,
italic, inline code), `alt+l` (choose a code block's language), `ctrl+backspace`, and
`tab` (indent). A host that uses one of them, such as `ctrl+c` to quit or `tab` to change
panel, must take it before the block does, as in the `switch` above.

Paste is the terminal's: `ctrl+v` makes the terminal send the clipboard as a
`tea.PasteMsg`, which the block inserts when the host passes it on.

## Popups over the block

When the host draws anything over the block's cells (a dialog, a menu, a help screen),
call `block.Cover(true)` and return its command. The block then stops drawing, and the
host's text stays visible. Call `block.Cover(false)` when the popup closes; the block
draws itself again.

## Reading the text, drafts and sending

```go
text, rev := ed.Text()              // the Markdown and its revision; does not wait
md, err := ed.Markdown(ctx)         // the Markdown, asked from the page
out, err := ed.Export(ctx, "jira")  // out.Body is ADF JSON, out.Mime "application/json"
ed.SetMarkdown(text)                // replace the document, for example with a draft
ed.Modified(); ed.MarkSaved()       // changed since the start or the last MarkSaved
```

Markdown is the editor's own format and loses nothing, so store drafts as Markdown and
convert to the destination's format only when sending.

`ed.Events()` is a channel. Read it in a command that the host starts again after each
event, as `examples/embed/main.go` does:

- `slate.Changed{Rev, Origin}` after every change. `Origin` is `"user"`, `"host"`, or the
  name another program gave through the link. This is the place to save a draft.
- `slate.Failed{Op, Err}` when a call that returns nothing could not be carried out.
- `slate.Restarted{}` when the browser stopped and slate opened the document again. The
  host has nothing to do; the text, cursor and place are restored.

## Editing an existing Jira comment

```go
block, _ := bubble.Open(bubble.Config{Options: slate.Options{Target: "jira"}})
left, err := block.Editor().Import(ctx, adfJSON) // the comment's body as Jira returned it
```

`Import` converts Atlassian Document Format to Markdown and loads it. `left` names what
the comment held that Markdown has no form for and the editor therefore does not show.
For Jira that is `"media"`: attached images and files. Sending the edited text back over
the original removes them, so warn the user when `left` is not empty.

Text colour and underline are dropped without a report. An emoji, a status lozenge, a
date and a smart link are kept as plain text or an ordinary link.

`ed.Unsupported(ctx)` lists what the current text holds that the target cannot carry
(for Jira: images, which are sent as links, and HTML, which is sent as text). The editor
underlines those stretches with a wavy line.

## Mentions

`Options.Mentions` is called with the text typed after `@`. Return the matching users as
`slate.Mention{ID, Name}`; for Jira, `ID` is the account ID. The editor stores a mention
as `[@Name](mention:accountId)` and the Jira export writes a `mention` node.

## The link

Every block is listed for other programs of the same user, so that a language model in
another pane can read the draft and propose text while the user watches:

```
slate ls
slate read ID
slate edit -old TEXT -new TEXT -by claude ID
```

An edit made this way shows in the editor at once, highlighted, and one undo removes it.
The link can read and replace text. It has no request that saves or sends.

`Config.Title` and `Config.Meta` are what `slate ls` shows, so give each block a title a
person or a model can pick it by. `Config.Private: true` keeps one block out of the link.
`SLATE_LINK=0` in the environment keeps all of them out.

## Several blocks

Each `bubble.Open` makes an independent editor, and all editors of a process share one
browser. One block on screen at a time is tested, including inside Herdr. Two blocks
visible at once inside Herdr are not tested: each would start its own helper process
writing to the same console. Close a block before opening the next, or keep one block
and swap its text with `SetMarkdown`.

## Checklist

- `block.Init()` is returned from the host's `Init`.
- Every unconsumed message reaches `block.Update`, and its command is returned.
- `tea.WindowSizeMsg` reaches both `SetRect` and `block.Update`.
- Nothing but `block.View()` is drawn in the rectangle, except under `Cover(true)`.
- `block.Close()` runs before the program exits. Inside Herdr it removes the image.
- Host keys such as save and quit are taken before the block sees them.

## Troubleshooting

- The box is empty: the terminal does not show Sixel, or the host draws over the cells,
  or messages do not reach `block.Update`.
- A key does nothing: run the demo host with `SLATE_KEYLOG=keys.log` and read what the
  terminal reported for that key. Terminal settings can remap keys before slate sees them.
- Inside Herdr, set `SLATE_HERDR_LOG=herdr.log` to see where the image is drawn and when
  it is restored. `README.md` describes how the Herdr path works and its limits.
