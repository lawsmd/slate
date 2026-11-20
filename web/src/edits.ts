// Changes made from outside the keyboard: who made each change, and a
// highlight on text another program wrote.

import { Annotation, StateEffect, StateField } from "@codemirror/state";
import { Decoration, type DecorationSet, EditorView } from "@codemirror/view";
import { isolateHistory } from "@codemirror/commands";

/** Who made a change: "user" when absent, "host", or a name the host gave. */
export const origin = Annotation.define<string>();

/** How long text from an edit stays highlighted. */
const highlightMs = 4000;

const addMark = StateEffect.define<{ from: number; to: number; id: number }>();
const clearMark = StateEffect.define<number>();
let nextMark = 0;

const mark = (id: number) => Decoration.mark({ class: "sl-edited", id });

/** The highlighted ranges. They move with later changes. */
export const editMarks = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(marks, tr) {
    marks = marks.map(tr.changes);
    for (const e of tr.effects) {
      if (e.is(addMark) && e.value.to > e.value.from) {
        marks = marks.update({ add: [mark(e.value.id).range(e.value.from, e.value.to)] });
      } else if (e.is(clearMark)) {
        marks = marks.update({ filter: (_from, _to, deco) => deco.spec.id !== e.value });
      }
    }
    return marks;
  },
  provide: (field) => EditorView.decorations.from(field),
});

/** The text each writer's latest edit put in, by writer, mapped through later changes. */
const setWritten = StateEffect.define<{ by: string; from: number; to: number }>();

export const written = StateField.define<Map<string, { from: number; to: number }>>({
  create: () => new Map(),
  update(ranges, tr) {
    if (!tr.docChanged && !tr.effects.some((e) => e.is(setWritten))) return ranges;
    const next = new Map<string, { from: number; to: number }>();
    for (const [by, r] of ranges) {
      // Text typed inside the range joins it; text typed against either end stays outside.
      const from = tr.changes.mapPos(r.from, 1);
      const to = tr.changes.mapPos(r.to, -1);
      if (to > from) next.set(by, { from, to });
    }
    for (const e of tr.effects) {
      if (!e.is(setWritten)) continue;
      if (e.value.to > e.value.from) next.set(e.value.by, { from: e.value.from, to: e.value.to });
      else next.delete(e.value.by);
    }
    return next;
  },
});

/** Each writer's text as it stands now. */
export function writtenText(view: EditorView): { by: string; text: string }[] {
  return [...view.state.field(written)].map(([by, r]) => ({ by, text: view.state.sliceDoc(r.from, r.to) }));
}

export interface Edit {
  /** Text to find. It must occur exactly once. Empty adds `new` at the end. */
  old: string;
  new: string;
  /** Replace the text this writer's latest edit put in, as it stands now, instead of `old`. */
  own?: boolean;
  /** Refuse the edit unless the document is at this revision. */
  ifRev?: number;
}

/**
 * Replaces one exact piece of text. The change is one undo step, the cursor
 * keeps its place in the text around it, and the new text is highlighted for
 * a few seconds. Throws when `old` is absent or occurs more than once, when
 * `own` is set and the writer has no text, or when `ifRev` is not `rev`.
 */
export function applyEdit(view: EditorView, edit: Edit, by: string, rev: number): void {
  if (edit.ifRev !== undefined && edit.ifRev !== rev) {
    throw new Error(`the document has changed since revision ${edit.ifRev}; read it again`);
  }
  const doc = view.state.doc.toString();
  let from = doc.length;
  let to = doc.length;
  if (edit.own) {
    const r = view.state.field(written).get(by);
    if (!r) throw new Error(`${by} has no text in the document`);
    ({ from, to } = r);
  } else if (edit.old !== "") {
    from = doc.indexOf(edit.old);
    if (from < 0) throw new Error("the text to replace is not in the document");
    if (doc.indexOf(edit.old, from + 1) >= 0) throw new Error("the text to replace occurs more than once");
    to = from + edit.old.length;
  }
  const id = ++nextMark;
  view.dispatch({
    changes: { from, to, insert: edit.new },
    annotations: [origin.of(by), isolateHistory.of("full")],
    effects: [addMark.of({ from, to: from + edit.new.length, id }), setWritten.of({ by, from, to: from + edit.new.length })],
    userEvent: "input.edit",
  });
  setTimeout(() => view.dispatch({ effects: clearMark.of(id) }), highlightMs);
}
