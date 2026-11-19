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

export interface Edit {
  /** Text to find. It must occur exactly once. Empty adds `new` at the end. */
  old: string;
  new: string;
}

/**
 * Replaces one exact piece of text. The change is one undo step, the cursor
 * keeps its place in the text around it, and the new text is highlighted for
 * a few seconds. Throws when `old` is absent or occurs more than once.
 */
export function applyEdit(view: EditorView, edit: Edit, by: string): void {
  const doc = view.state.doc.toString();
  let from = doc.length;
  let to = doc.length;
  if (edit.old !== "") {
    from = doc.indexOf(edit.old);
    if (from < 0) throw new Error("the text to replace is not in the document");
    if (doc.indexOf(edit.old, from + 1) >= 0) throw new Error("the text to replace occurs more than once");
    to = from + edit.old.length;
  }
  const id = ++nextMark;
  view.dispatch({
    changes: { from, to, insert: edit.new },
    annotations: [origin.of(by), isolateHistory.of("full")],
    effects: addMark.of({ from, to: from + edit.new.length, id }),
    userEvent: "input.edit",
  });
  setTimeout(() => view.dispatch({ effects: clearMark.of(id) }), highlightMs);
}
