# slate

Working name. A rich-text editing block that runs inside a terminal application.

## Goal

A rectangle in a terminal UI where Markdown renders live as it is typed, the way Obsidian's
live preview does: syntax hides, headings are larger, code blocks have a tinted background,
a language selector and line numbers. Inside the rectangle the content does not have to
look like a terminal. Outside it, the host application draws its usual cells.

## How it works

1. A headless Chromium (Edge on Windows 11, driven over the DevTools protocol) runs a web
   editor.
2. The host forwards the keystrokes and mouse clicks that fall in the rectangle to that page.
3. After each change, slate captures the page's frame, encodes it as Sixel and writes it
   into the rectangle.

## Uses

- Notes, saved as Markdown.
- Comments written in a host application and sent to a ticketing platform. The first host
  is tui-q, sending Jira comments as Atlassian Document Format.
- Colours and font set to match the terminal theme.

## Document model

slate keeps one document model and exports from it: Markdown for notes, Atlassian Document
Format for Jira. A code block with a language maps to the `codeBlock` node.

## Shape

A separate project under its own version control. Host applications import it. tui-q uses
it through a generic hook and holds none of slate's code.

## First test

Headless Edge, one editable page, Sixel written to a fixed rectangle in Windows Terminal
1.22 or later. Measure the delay between a keystroke and the redrawn frame. If typing feels
slow, the design does not work as planned.

Run it in Windows Terminal:

```
go run ./cmd/slate-bench            # 200 synthetic keystrokes per capture mode, then a table
go run ./cmd/slate-bench -manual    # type by hand; ctrl+c quits
```

Result on the development machine (Edge 157, Windows Terminal 1.24, an 80 by 24 cell
block of 800 by 480 pixels), from key event to the terminal's reply to a cursor position
request sent after the image:

| Mode | Median | 95th percentile |
| --- | --- | --- |
| screenshot per key, whole block redrawn | 46 ms | 61 ms |
| screenshot per key, changed rows redrawn | 30 ms | 43 ms |
| screencast, whole block redrawn | 29 ms | 32 ms |
| screencast, changed rows redrawn | 13 ms | 18 ms |

slate uses the last mode. The reply shows the terminal has read the image. It does not
show that the image is on screen, so judge the feel with `-manual`.

## Demo host

```
go build -o bin/slate.exe ./cmd/slate
bin\slate notes file.md          # edit a note; ctrl+s saves
bin\slate comment -o out.json    # write a comment; ctrl+s writes it as ADF
bin\slate shot -o out.png file.md  # render to a PNG without a terminal
```

`f1` lists the keys. `ctrl+q` quits.

## Using slate from a host

```go
ed, err := slate.New(slate.Options{Markdown: text, Mentions: searchUsers})
defer ed.Close()

ed.SetRect(col, row, cols, rows)  // terminal cells, counted from 1
ed.SetCellSize(w, h)              // pixels, from the terminal's reply to CSI 16 t
ed.Key(slate.Key{Name: "a"})      // also Mouse, Paste, Copy, Cut, Focus

<-ed.Updates()                    // the picture changed
os.Stdout.Write(ed.Draw())        // Sixel for the changed rows

md, _ := ed.Markdown()
adf, _ := ed.ADF()                // the body of a Jira Cloud comment
```

The host must leave the block's cells alone between draws: text written over a cell
erases the image there. After a popup over the block closes, or the screen is cleared,
call `ed.Invalidate()` and draw again.

A Bubble Tea v2 host uses the `bubble` package, which does this bookkeeping. See
`cmd/slate/host.go`.

## Herdr

Herdr 0.8.2 passes Kitty graphics to the outer terminal and no Sixel. Windows Terminal
shows Sixel and no Kitty graphics. A slate block in a Herdr pane on Windows Terminal is
therefore drawn around Herdr:

1. A helper process attaches to the console of Herdr's client (`AttachConsole`), which is
   the Windows Terminal tab itself, and writes the Sixel there.
2. Herdr's socket API (`session.snapshot` and `events.subscribe` on the named pipe at
   `HERDR_SOCKET_PATH`) gives the pane's cells in that terminal and says whether the
   pane's tab is showing.
3. Herdr rewrites only cells whose text changes, so pieces of the image stay in cells that
   are blank before and after a tab switch, a split or the editor's exit. slate reads the
   console's cells and writes a space over each blank one in the old rectangle.
4. With its default settings Herdr repaints every cell 50 to 60 ms after its window gets
   the keyboard focus back, and that removes the image. Before a whole image, slate writes
   a space with a blue foreground to each cell of the block. The image hides those cells,
   and the console keeps their colour until Herdr writes over them. The helper reads the
   cells every 4 ms, and every millisecond for 150 ms after the terminal's window comes to
   the front. When some have lost the colour and all of those are blank, the helper writes
   the last whole image again and slate follows with the current one. While any cell holds
   text, a popup covers the block, and slate waits for it to go.
5. slate sends the helper the whole image 250 ms after the last changed rows, so the image
   the helper writes after a wipe is the current one.
6. When the pane reports that it has the keyboard focus again, slate also writes the
   whole image 200 ms and one second later, for a loss the helper cannot see.

```go
if herdr.Inside() {
    pane, err := herdr.Open()
    defer pane.Close()
    block.Through(bubble.Herdr(pane)) // block is a *bubble.Model
}
```

`bubble.Model` writes the image through a `bubble.Painter`. The default painter writes
through Bubble Tea. `bubble.Herdr` is the painter for a Herdr pane, and another
multiplexer needs only another painter.

### Focus blink

Herdr's repaint on focus shows the block empty for one screen refresh on some returns to
the window: Herdr ends its frame, the terminal may paint, and the image follows about 6 ms
later. Herdr has a setting that turns the repaint off. In Herdr's `config.toml`:

```toml
[ui]
redraw_on_focus_gained = false
```

Apply it with `herdr server reload-config`. With the setting off, the image stays in
place when the window returns to the front and the block does not blink (measured on
Herdr 0.8.2: four returns to the front, no wipe). Herdr uses the repaint to clear display
corruption left while the window was in the background, so with the setting off such
corruption stays until Herdr's next full redraw.

The helper is the host's own executable, started again with `SLATE_HERDR_HELPER` set. The
`herdr` package's `init` handles that, so the host needs no code for it.

A Herdr pane answers no colour query, so the demo host reads the colour scheme and font
from Windows Terminal's `settings.json` with `slate.WindowsTerminalTheme()`, and reads it
again every two seconds to follow a change of scheme.

Limits found so far:

- A Herdr pane does not answer `CSI 16 t`, so slate assumes a cell of 10 by 20 pixels,
  which is what Windows Terminal reports at every font size.
- With `redraw_on_focus_gained` on, the block is empty for about 6 ms after Herdr
  repaints on focus: 1 to 2 ms until the helper has seen the same cells twice, and about
  5 ms to write the image. Acting on the first look could put a write inside Herdr's
  output. Herdr wraps each frame in a synchronized update (`CSI ?2026`), which it ends
  itself, so slate cannot hold the terminal's painting across the gap.
- Herdr reports the cell size (`pane.graphics.info`) only with
  `experimental.kitty_graphics` turned on. slate uses the answer when it gets one.
- If Herdr repaints while a popup covers part of the block, the rest of the block stays
  empty until the popup closes.
- slate and Herdr write to the same console without coordination. A write from slate that
  falls inside one of Herdr's escape sequences would garble it. None was seen in testing.
- With two clients attached to one Herdr session, slate draws in the first it finds.

Set `SLATE_HERDR_LOG` to a file path and the helper writes there what it finds in the
marked cells: `marked`, `wiped`, or `covered` with the cell that holds text.

`cmd/slate-probe` holds the experiments behind this: `-pid` writes a test image to another
process's console, `-queries` lists what the current terminal answers, `-api` sends one
request to Herdr's socket, `-focus` tells Herdr its window lost the focus and got it back,
and `-cells` prints the characters and attributes the console holds.

## Rendering styles

`Options.Render` sets a style per construct: `slate.Live` (syntax hides until the cursor
reaches it, as in Obsidian), `slate.Block` (a box or widget, as in Jira) or `slate.Raw`.
Code blocks and tables default to `Block`, everything else to `Live`. In `Block` style a
code block's fence line shows while the cursor is on that line, which is how the language
is typed by hand; `alt+l` or a click on the language label opens the language list.

## Markdown and ADF

| Markdown | ADF |
| --- | --- |
| paragraphs, `#` headings, `>` quotes, `---` | `paragraph`, `heading`, `blockquote`, `rule` |
| `**`, `*`, `~~`, `` ` ``, `[text](url)` | `strong`, `em`, `strike`, `code`, `link` marks |
| `-` and `1.` lists | `bulletList`, `orderedList` |
| `- [ ]` and `- [x]` | `taskList`, `taskItem` |
| fenced code with a language | `codeBlock` with `language` |
| pipe tables | `table` |
| Obsidian callouts, `> [!info]` | `panel` |
| `[@Name](mention:accountId)` | `mention` |

A line break inside a paragraph exports as a `hardBreak`, because the editor shows it as
a new line.

## Layout

| Path | Contents |
| --- | --- |
| `slate.go`, `draw.go`, `input.go`, `theme.go` | the `Editor` |
| `wt.go` | colours and font from Windows Terminal's settings |
| `browser/` | finds and starts Edge, DevTools protocol over a pipe |
| `sixel/` | colour quantiser and Sixel encoder |
| `bubble/` | Bubble Tea v2 adapter |
| `herdr/` | draws from inside a Herdr pane (Windows) |
| `term/` | raw terminal access, used by the bench |
| `web/` | the editor page: CodeMirror 6, live preview, ADF export |
| `web/dist/index.html` | the built page, embedded in the Go package |
| `cmd/slate/`, `cmd/slate-bench/` | demo host and latency test |
| `cmd/slate-probe/` | experiments for drawing under a multiplexer |

After changing anything under `web/src`, run `npm run build` in `web/` (or `go generate`)
and rebuild. Tests: `go test ./...` and `npm test` in `web/`.

## Known costs

- Every layer between the application and the screen must pass Sixel through. Windows
  Terminal panes do. tmux does only when built with Sixel support, and the demo host
  refuses to start under tmux or Zellij unless `SLATE_FORCE=1` is set. Herdr does not pass
  Sixel either, and slate writes around it (see "Herdr").
- Text in the block is an image. Terminal selection, copy and search do not see it, so
  slate needs its own copy and paste.
- The browser cannot read the terminal's font or colours. They are configured or queried.
- Mouse clicks arrive per cell.
- A host that draws in cells (Bubble Tea) writes the image after each frame and needs
  special handling for anything drawn over the block.
