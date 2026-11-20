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

`f1` lists the keys. `ctrl+q` quits. With `SLATE_KEYLOG` set to a file path, the demo
host writes there each key press as the terminal reported it.

## Using slate from a host

A Bubble Tea v2 program embeds slate with one call, `bubble.Open`, and four methods of
the block it returns. [EMBEDDING.md](EMBEDDING.md) is the guide, and
`examples/embed/main.go` is a complete small host.

A host on another framework drives the editor directly:

```go
slate.Warm()                      // at startup: start the browser in the background
defer slate.Shutdown()

ed, err := slate.New(slate.Options{Markdown: text, Target: "jira", Mentions: searchUsers})
defer ed.Close()

ed.SetRect(col, row, cols, rows)  // terminal cells, counted from 1
ed.SetCellSize(w, h)              // pixels, from the terminal's reply to CSI 16 t
ed.Key(slate.Key{Name: "a"})      // also Mouse, Paste, Focus, SetTheme

<-ed.Updates()                    // the picture changed
os.Stdout.Write(ed.Draw())        // Sixel for the changed rows

md, _ := ed.Markdown(ctx)
out, _ := ed.Export(ctx, "")      // the document in its target's form
```

The host must leave the block's cells alone between draws: text written over a cell
erases the image there. After a popup over the block closes, or the screen is cleared,
call `ed.Invalidate()` and draw again. The `bubble` package does this bookkeeping.

### Calls and the browser

Every editor of a process is a page in one browser. The first `New` starts the browser
(about 0.8 s measured) unless `Warm` did so earlier; a second editor opens in about 0.3 s.

`Key`, `Mouse`, `Paste`, `Focus`, `SetRect`, `SetCellSize`, `SetTheme`, `SetRender`,
`SetTarget` and `SetMarkdown` return at once. slate sends them to the page in the order
they were called. `Markdown`, `Export`, `Edit`, `Snapshot`, `Tree`, `Copy`, `Cut` and
`Screenshot` wait for the page, after every call made before them, and take a context.

`ed.Events()` delivers `slate.Changed{Rev, Origin}` for each change of the document,
`slate.Failed{Op, Err}` when one of the calls that return nothing could not be carried
out, and `slate.Restarted` when the browser or the page stopped and slate opened a new
page with the same document, cursor, theme and place. `ed.Text()` returns the document and
its revision as the page last reported them, without asking the page.

`ed.Edit(ctx, slate.Edit{Old, New}, "name")` replaces one exact piece of text for a writer
other than the user. The change is one undo step, the user's cursor keeps its place, and
the new text is highlighted for four seconds.

## Herdr

Herdr shows an image that a pane writes as Kitty graphics, and passes no Sixel to the
outer terminal. Windows Terminal shows Sixel and no Kitty graphics. A slate block in a
Herdr pane on Windows Terminal is therefore drawn around Herdr. slate follows the latest
Herdr and was last tested on Herdr 0.9.5. After updating Herdr, run `go test ./herdr` in a
Herdr pane: it fails when Herdr no longer has a call, an event or a field slate uses.

1. A helper process attaches to the console of Herdr's client (`AttachConsole`), which is
   the Windows Terminal tab itself, and writes the Sixel there.
2. Herdr's socket API (`session.snapshot` and `events.subscribe` on the named pipe at
   `HERDR_SOCKET_PATH`) gives the pane's cells in Herdr's pane area and says whether the
   pane's tab is showing. Herdr counts those cells from the top left of the pane area,
   which leaves out the sidebar and the tab bar, and reports nowhere where the area
   starts. The helper reads the console's size and reports it to the host. slate takes the
   pane area to fill the bottom right of the terminal, with the sidebar to its left and the
   tab bar above, and adds the difference in size to the pane's cells.
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

`bubble.Open` does this for a host. By hand it is:

```go
if herdr.Inside() {
    pane, err := herdr.Open()
    defer pane.Close()
    block.Through(bubble.Herdr(pane)) // block is a *bubble.Model
}
```

`bubble.Model` writes the image through a `bubble.Painter`. The default painter writes
through Bubble Tea, and `bubble.Herdr` is the painter for a Herdr pane.

### Other multiplexers

`bubble.Open` looks through a list of hosts (`bubble.Host`) for the one the program runs
in. slate's list holds tmux and Zellij, which it refuses, and Herdr. A program that runs in
another multiplexer writes a painter for it and adds a host before the first `Open`:

```go
bubble.Register(bubble.Host{
    Name:   "mymux",
    Detect: func() bool { return os.Getenv("MYMUX") != "" },
    Open:   func() (bubble.Painter, error) { return newMymuxPainter() },
    Mute:   true, // the pane answers no colour query
})
```

A registered host is tried before slate's own. `bubble.Config.Painter` skips the search and
uses the painter it names.

### Focus blink

Herdr's repaint on focus shows the block empty for one screen refresh on some returns to
the window: Herdr ends its frame, the terminal may paint, and the image follows about 6 ms
later. Herdr has a setting that turns the repaint off. In Herdr's `config.toml`:

```toml
[ui]
redraw_on_focus_gained = false
```

Apply it with `herdr server reload-config`. With the setting off, the image stays in
place when the window returns to the front and the block does not blink (measured: four
returns to the front, no wipe). Herdr uses the repaint to clear display
corruption left while the window was in the background, so with the setting off such
corruption stays until Herdr's next full redraw.

The helper is the host's own executable, started again with `SLATE_HERDR_HELPER` set. The
`herdr` package's `init` handles that, so the host needs no code for it.

A Herdr pane answers no colour query, so `bubble.Open` reads the colour scheme and font
from Windows Terminal's `settings.json` with `slate.WindowsTerminalTheme()`, and reads it
again every two seconds to follow a change of scheme.

Limits found so far:

- Herdr has no call that reports the pixel size of a cell. slate uses 10 by 20 pixels,
  the cell Windows Terminal draws Sixel on at every font size, and does not ask the pane
  with `CSI 16 t`, whose answer is for the pane and not for the terminal behind Herdr.
  `SLATE_CELL=WxH` sets another size.
- With `redraw_on_focus_gained` on, the block is empty for about 6 ms after Herdr
  repaints on focus: 1 to 2 ms until the helper has seen the same cells twice, and about
  5 ms to write the image. Acting on the first look could put a write inside Herdr's
  output. Herdr wraps each frame in a synchronized update (`CSI ?2026`), which it ends
  itself, so slate cannot hold the terminal's painting across the gap.
- A Herdr layout with the sidebar on the right, or a bar below the pane area, would put
  the image in the wrong place.
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

## Targets

A target is where the document goes when it is written: a Jira comment, a Markdown file,
later an Obsidian note. `Options.Target` names it. The target sets the Markdown extensions
the editor parses with, the defaults of the live view, and the export. The editor and the
export parse with the same extensions.

| Target | Export |
| --- | --- |
| `markdown` (default) | the Markdown text, `text/markdown` |
| `jira` | Atlassian Document Format, `application/json` |

`ed.Export(ctx, "")` returns the document in the editor's target's form, and
`ed.Export(ctx, "jira")` in a named one's. `ed.Import(ctx, body)` loads a document from
the target's form, such as a Jira comment fetched for editing, and returns the names of
what it had to leave out. For `jira` that is `media`, the attached images and files, which
Markdown has no form for. `ed.ADF(ctx)` is `Export` for `jira`.

A target also lists what it cannot carry as written. The editor underlines each such
stretch with a wavy line, and `ed.Unsupported(ctx)` returns them with what the export
makes of each. For `jira` these are images (sent as links) and HTML (sent as text).

### Adding a target

Write one file in `web/src/targets/` that exports a `Target` (see `index.ts` for the
interface and `markdown.ts` for the smallest one), add it to `targets` in `index.ts`, run
`npm run build` in `web/`, and rebuild the Go program. `export` receives the text and its
Lezer syntax tree.

A host can also write an export in Go. `ed.Tree(ctx)` returns the syntax tree as JSON with
byte offsets into the Markdown:

```json
{"markdown": "Hi *you*", "root": {"type": "Document", "from": 0, "to": 8, "children": [...]}}
```

## Link

Package `link` lets another program of the same user read and edit the documents open in
slate editors, so that a person and a language model can write one together. A `bubble`
host registers its editor when it starts. `SLATE_LINK=0` in the host's environment turns
that off for every editor of the host, and `bubble.Config.Private` for one.

```
slate ls                                   # the editors open on this machine
slate read [ID]                            # an editor's Markdown; -json adds revision, selection
                                           # and the text each writer last put in
slate edit -old TEXT -new TEXT [-by NAME] [-if-rev N] [ID]
slate edit -own -new-file draft.md -by NAME [-if-rev N] [ID]
slate watch [ID]                           # one line per change
```

`ID` may be left out while one editor is open. `edit` replaces text that occurs exactly
once, and without `-old` adds the new text at the end. The edit shows in the editor at
once, highlighted, and one undo removes it.

- `-new-file` and `-old-file` read the text from a file, or from standard input when
  given `-`. Use them for Markdown with quotes or backticks, and for text longer than a
  command line allows.
- `-own` replaces the text the writer named by `-by` last put in, as it stands now. Text
  the person typed inside that text goes with it; text typed before or after it stays.
  `slate read -json` lists each writer's text under `written`, so the writer can see what
  the person changed before it replaces it.
- `-if-rev N` refuses the edit unless the document is still at revision `N`, as
  `read -json` reported it. A writer that reads, then edits with the revision it read,
  cannot overwrite a change the person made in between.

The link can read and replace text and nothing else. It has no request that saves, sends
or submits a document, so a comment reaches Jira only when the person presses the host's
own key for that.

A host tells clients what a document is with `block.Describe("comment on PROJ-123",
map[string]string{"ticket": "PROJ-123"})`. `slate ls` shows the title, the fields, the
target, the host program and its Herdr pane.

Each host listens on a Unix socket in `%LOCALAPPDATA%\slate\run` (the user's cache
directory on other systems) and writes a JSON file beside it that lists its editors. Each
request is one line of JSON on a new connection: `list`, `read`, `edit` or `watch`. Go
programs use `link.List`, `link.Read`, `link.Edit` and `link.Watch`. There is no MCP
server yet; an agent with a shell uses the commands above.

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

The import reads the same table from right to left. It also keeps the text of an emoji,
a status, a date and a smart link, drops text colour and underline, and leaves out
attachments (`web/src/targets/jira-import.ts`).

## Layout

| Path | Contents |
| --- | --- |
| `slate.go`, `draw.go`, `input.go`, `theme.go` | the `Editor` |
| `pool.go` | the browser that a process's editors share |
| `wt.go` | colours and font from Windows Terminal's settings |
| `browser/` | finds and starts Edge, DevTools protocol over a pipe |
| `sixel/` | colour quantiser and Sixel encoder |
| `bubble/` | Bubble Tea v2 adapter: `Open`, the block, its painters |
| `examples/embed/` | a small host with a comment box in a panel |
| `EMBEDDING.md` | how to put a block in another program |
| `link/` | reading and editing open editors from another program |
| `herdr/` | draws from inside a Herdr pane (Windows) |
| `term/` | raw terminal access, used by the bench |
| `web/` | the editor page: CodeMirror 6, live preview |
| `web/src/targets/` | one file per downstream format |
| `web/dist/index.html` | the built page, embedded in the Go package |
| `cmd/slate/` | demo host, and the `ls`, `read`, `edit` and `watch` commands |
| `cmd/slate-bench/` | latency test |
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
