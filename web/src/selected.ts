// CodeMirror draws the selection in a layer behind the text, so any line or
// span with an opaque background hides it. Code blocks and inline code have
// one. This plugin marks the selected text, and the lines selected from end
// to end, in the text itself; style.css paints those marks only where the
// layer behind would be hidden.

import { type EditorState, type Range } from "@codemirror/state";
import { Decoration, type DecorationSet, type EditorView, ViewPlugin, type ViewUpdate } from "@codemirror/view";

const text = Decoration.mark({ class: "sl-sel" });
const whole = Decoration.line({ class: "sl-sel-line" });

/**
 * The marks for the selection between `from` and `to` in the document. A line
 * counts as selected end to end when the selection starts at or before its
 * start and runs past its end, or reaches the end of the document.
 */
export function selectedMarks(state: EditorState, from = 0, to = state.doc.length): Range<Decoration>[] {
  const out: Range<Decoration>[] = [];
  const end = state.doc.length;
  for (const r of state.selection.ranges) {
    if (r.empty || r.to < from || r.from > to) continue;
    let pos = Math.max(r.from, from);
    const stop = Math.min(r.to, to);
    while (pos <= stop) {
      const line = state.doc.lineAt(pos);
      const a = Math.max(r.from, line.from);
      const b = Math.min(r.to, line.to);
      if (r.from <= line.from && (r.to > line.to || r.to === end)) out.push(whole.range(line.from));
      if (b > a) out.push(text.range(a, b));
      if (line.to >= stop) break;
      pos = line.to + 1;
    }
  }
  return out;
}

function build(view: EditorView): DecorationSet {
  const out: Range<Decoration>[] = [];
  for (const { from, to } of view.visibleRanges) out.push(...selectedMarks(view.state, from, to));
  return Decoration.set(out, true);
}

export const showSelection = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = build(view);
    }
    update(u: ViewUpdate) {
      if (u.selectionSet || u.docChanged || u.viewportChanged) this.decorations = build(u.view);
    }
  },
  { decorations: (p) => p.decorations },
);
