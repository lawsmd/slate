// Keys for lists and code blocks that behave as they do in Jira.
//
// Lists: Enter on an empty item ends the list on that line, and Shift+Enter
// adds a line inside the item, so Shift+Enter then Enter starts the next
// item one blank line down.
//
// Code blocks, in the `block` style: the cursor never stands on a fence
// line, the arrow keys step over the fences, Backspace in an empty block
// deletes it, and Mod-a selects the block's text before the whole document.

import { deleteMarkupBackward, insertNewlineContinueMarkupCommand } from "@codemirror/lang-markdown";
import { ensureSyntaxTree, syntaxTree } from "@codemirror/language";
import {
  EditorSelection,
  EditorState,
  type Line,
  Prec,
  type SelectionRange,
  type StateCommand,
  type Transaction,
  type TransactionSpec,
} from "@codemirror/state";
import { type Command, keymap, ViewPlugin } from "@codemirror/view";
import type { SyntaxNode } from "@lezer/common";
import flourite from "flourite";
import { renderConfig } from "./config";
import { languageName } from "./langpicker";

const tree = (state: EditorState) => ensureSyntaxTree(state, state.doc.length, 200) ?? syntaxTree(state);

/** A fenced code block with both fences and at least one line between them. */
export interface Block {
  open: Line;
  close: Line;
  first: Line;
  last: Line;
  node: SyntaxNode;
}

function closedBlock(state: EditorState, node: SyntaxNode): Block | null {
  const marks = node.getChildren("CodeMark");
  if (marks.length < 2) return null;
  const { doc } = state;
  const open = doc.lineAt(node.from);
  const close = doc.lineAt(marks[marks.length - 1].from);
  if (close.number < open.number + 2) return null;
  return { open, close, first: doc.line(open.number + 1), last: doc.line(close.number - 1), node };
}

/** The closed code block whose lines, fences included, hold pos. */
export function blockAt(state: EditorState, pos: number): Block | null {
  const line = state.doc.lineAt(pos);
  let found: Block | null = null;
  tree(state).iterate({
    from: line.from,
    to: line.to,
    enter(ref) {
      if (found) return false;
      if (ref.name !== "FencedCode") return;
      const b = closedBlock(state, ref.node);
      if (b && b.open.number <= line.number && line.number <= b.close.number) found = b;
      return false;
    },
  });
  return found;
}

const blockStyle = (state: EditorState) => state.facet(renderConfig).code === "block";

const isContent = (b: Block, pos: number) => pos >= b.first.from && pos <= b.last.to;

// ---- Keeping the cursor off fences ----

/**
 * Where a selection end that lands on a fence goes instead. `from` is where
 * it came from; `other` is the selection's other end. A selection reaching
 * out of the block, such as select-all, keeps its ends.
 */
function offFence(state: EditorState, pos: number, from: number | null, other: number): number {
  const b = blockAt(state, pos);
  if (!b) return pos;
  if (other < b.open.from || other > b.close.to) return pos;
  const { doc } = state;
  if (pos >= b.open.from && pos <= b.open.to) {
    // Moving back out of the block's first line goes to the line above it.
    if (from !== null && from > b.open.to && b.open.number > 1) return doc.line(b.open.number - 1).to;
    return b.first.from;
  }
  if (pos >= b.close.from && pos <= b.close.to) {
    // Moving on from the block's last line goes to the line below it.
    if (from !== null && from < b.close.from && b.close.number < doc.lines) return doc.line(b.close.number + 1).from;
    return b.last.to;
  }
  return pos;
}

const fenceFilter = EditorState.transactionFilter.of((tr): Transaction | readonly (Transaction | TransactionSpec)[] => {
  if (!tr.selection && !tr.docChanged) return tr;
  const state = tr.state;
  if (!blockStyle(state)) return tr;
  const pointer = tr.isUserEvent("select.pointer");
  const before = tr.startState.selection.ranges;
  const after = state.selection.ranges;
  let moved = false;
  const ranges = after.map((r: SelectionRange, i: number) => {
    const old = !pointer && before.length === after.length ? before[i] : null;
    const head = offFence(state, r.head, old ? tr.changes.mapPos(old.head) : null, r.anchor);
    const anchor = r.empty ? head : offFence(state, r.anchor, old ? tr.changes.mapPos(old.anchor) : null, r.head);
    if (head === r.head && anchor === r.anchor) return r;
    moved = true;
    return EditorSelection.range(anchor, head);
  });
  if (!moved) return tr;
  return [tr, { selection: EditorSelection.create(ranges, state.selection.mainIndex), sequential: true }];
});

/**
 * Up from a block that starts the document, or Down from one that ends it,
 * adds an empty line there so the cursor can leave the block. `landed` is
 * the line an ordinary move would reach.
 */
export function edgeLine(state: EditorState, forward: boolean, landed: number): TransactionSpec | null {
  const r = state.selection.main;
  if (state.selection.ranges.length > 1 || !r.empty || !blockStyle(state)) return null;
  const b = blockAt(state, r.head);
  if (!b || !isContent(b, r.head)) return null;
  const end = state.doc.length;
  if (forward && landed >= b.close.number && b.close.to === end) {
    return { changes: { from: end, insert: "\n" }, selection: { anchor: end + 1 }, userEvent: "input", scrollIntoView: true };
  }
  if (!forward && landed <= b.open.number && b.open.from === 0) {
    return { changes: { from: 0, insert: "\n" }, selection: { anchor: 0 }, userEvent: "input", scrollIntoView: true };
  }
  return null;
}

const arrow =
  (forward: boolean): Command =>
  (view) => {
    const { state } = view;
    const landed = state.doc.lineAt(view.moveVertically(state.selection.main, forward).head).number;
    const spec = edgeLine(state, forward, landed);
    if (!spec) return false;
    view.dispatch(spec);
    return true;
  };

/** Backspace at the start of a block, or just below one. */
export const codeBackspace: StateCommand = ({ state, dispatch }) => {
  const r = state.selection.main;
  if (state.selection.ranges.length > 1 || !r.empty || !blockStyle(state)) return false;
  const line = state.doc.lineAt(r.head);
  if (r.head !== line.from) return false;
  const inside = blockAt(state, r.head);
  if (inside && r.head === inside.first.from) {
    // An empty block goes; one with text in it stays.
    if (state.sliceDoc(inside.first.from, inside.last.to).trim() === "") {
      dispatch(
        state.update({
          changes: { from: inside.open.from, to: inside.close.to },
          selection: { anchor: inside.open.from },
          userEvent: "delete.backward",
          scrollIntoView: true,
        }),
      );
    }
    return true;
  }
  if (line.number < 2) return false;
  const above = blockAt(state, line.from - 1);
  if (!above || above.close.number !== line.number - 1) return false;
  // Never join a line onto the closing fence: step into the block instead,
  // taking an empty line along.
  dispatch(
    state.update({
      changes: line.length === 0 ? { from: above.close.to, to: line.to } : undefined,
      selection: { anchor: above.last.to },
      userEvent: line.length === 0 ? "delete.backward" : "select",
      scrollIntoView: true,
    }),
  );
  return true;
};

/** Delete at the end of a block, or just above one. */
export const codeDelete: StateCommand = ({ state, dispatch }) => {
  const r = state.selection.main;
  if (state.selection.ranges.length > 1 || !r.empty || !blockStyle(state)) return false;
  const line = state.doc.lineAt(r.head);
  if (r.head !== line.to) return false;
  const inside = blockAt(state, r.head);
  if (inside && r.head === inside.last.to) return true;
  if (line.number >= state.doc.lines) return false;
  const below = blockAt(state, line.to + 1);
  if (!below || below.open.number !== line.number + 1) return false;
  if (line.length === 0) {
    dispatch(
      state.update({
        changes: { from: line.from, to: line.to + 1 },
        selection: { anchor: below.first.from - line.length - 1 },
        userEvent: "delete.forward",
        scrollIntoView: true,
      }),
    );
  } else {
    dispatch(state.update({ selection: { anchor: below.first.from }, userEvent: "select", scrollIntoView: true }));
  }
  return true;
};

/** Mod-a in a code block selects its text; a second Mod-a falls through to select everything. */
export const selectBlock: StateCommand = ({ state, dispatch }) => {
  const r = state.selection.main;
  const b = blockAt(state, r.head);
  if (!b || !isContent(b, r.from) || !isContent(b, r.to)) return false;
  if (r.from === b.first.from && r.to === b.last.to) return false;
  dispatch(state.update({ selection: { anchor: b.first.from, head: b.last.to }, userEvent: "select" }));
  return true;
};

// ---- Lists ----

const continueMarkup = insertNewlineContinueMarkupCommand({ nonTightLists: false });

/** A list item's line: prefix (indent and quote marks), marker, space, and task box. */
const itemPattern = /^([\s>]*)([-*+]|(\d+)([.)]))(\s+)(\[[ xX]\]\s+)?/;

/** The list item a line belongs to, taken from the closest non-blank line at or above it. */
function itemAbove(state: EditorState, lineNo: number): RegExpExecArray | null {
  const { doc } = state;
  let n = lineNo;
  while (n >= 1 && /^\s*$/.test(doc.line(n).text)) n--;
  if (n < 1) return null;
  for (let node: SyntaxNode | null = tree(state).resolveInner(doc.line(n).to, -1); node; node = node.parent) {
    if (node.name === "FencedCode" || node.name === "CodeBlock") return null;
    if (node.name === "ListItem") return itemPattern.exec(doc.lineAt(node.from).text);
  }
  return null;
}

/** The marker for the item after `m`: the same bullet, or the next number. */
function nextMarker(m: RegExpExecArray): string {
  const marker = m[3] !== undefined ? String(Number(m[3]) + 1) + m[4] : m[2];
  return m[1] + marker + m[5] + (m[6] ? "[ ] " : "");
}

/**
 * Enter in a list. An empty item ends the list on its own line. On a blank
 * line made with Shift+Enter the next item starts below it. After an item with text,
 * the next item follows on the next line, with no blank line between.
 */
export const listEnter: StateCommand = ({ state, dispatch }) => {
  const r = state.selection.main;
  if (state.selection.ranges.length === 1 && r.empty) {
    const line = state.doc.lineAt(r.head);
    if (line.number > 1 && /^\s*$/.test(line.text)) {
      const m = itemAbove(state, line.number - 1);
      // Shift+Enter indents the line to the item's text; a line that ended
      // the list has no indent, and stays outside it.
      if (m && line.length >= m[0].length - (m[6]?.length ?? 0)) {
        // The blank line stays, emptied, and the next item goes below it.
        const insert = "\n" + nextMarker(m);
        dispatch(
          state.update({
            changes: { from: line.from, to: line.to, insert },
            selection: { anchor: line.from + insert.length },
            userEvent: "input",
            scrollIntoView: true,
          }),
        );
        return true;
      }
    }
  }
  let made: Transaction | null = null;
  if (!continueMarkup({ state, dispatch: (tr) => (made = tr) }) || !made) return false;
  dispatch(tightened(state, made));
  return true;
};

/** The same change without the blank line CodeMirror puts before a new item in a loose list. */
function tightened(state: EditorState, tr: Transaction): Transaction {
  const changes: { from: number; to: number; insert: string }[] = [];
  let removed = 0;
  tr.changes.iterChanges((from, to, _fromB, _toB, inserted) => {
    let text = inserted.toString();
    const blank = /^\n[ \t>]*\n/.exec(text);
    if (blank && removed === 0) {
      removed = blank[0].length - 1;
      text = "\n" + text.slice(blank[0].length);
    }
    changes.push({ from, to, insert: text });
  });
  if (removed === 0) return tr;
  const head = tr.selection!.main.head - removed;
  return state.update({ changes, selection: { anchor: head }, userEvent: "input", scrollIntoView: true });
}

/** Shift+Enter in a list item: a new line inside the item, indented to its text. */
export const listShiftEnter: StateCommand = ({ state, dispatch }) => {
  const r = state.selection.main;
  if (state.selection.ranges.length > 1 || !r.empty) return false;
  const line = state.doc.lineAt(r.head);
  const m = itemAbove(state, line.number);
  if (!m) return false;
  // A wrapped line of the item starts with its quote marks, then lines up with the text.
  const indent = m[1] + " ".repeat(m[0].length - m[1].length - (m[6]?.length ?? 0));
  let from = r.head;
  while (from > line.from && /[ \t]/.test(state.sliceDoc(from - 1, from))) from--;
  dispatch(
    state.update({
      changes: { from, to: r.head, insert: "\n" + indent },
      selection: { anchor: from + 1 + indent.length },
      userEvent: "input",
      scrollIntoView: true,
    }),
  );
  return true;
};

// ---- Language detection ----

/** The language name for a code snippet, or "" when it cannot tell. */
export function detectLanguage(code: string): string {
  const text = code.trim();
  if (text === "") return "";
  // flourite calls JSON JavaScript.
  if (/^[{[]/.test(text)) {
    try {
      JSON.parse(text);
      return "json";
    } catch {
      // not JSON
    }
  }
  const found = flourite(text, { noUnknown: false }).language;
  if (found === "Unknown") return "";
  // flourite takes shell and PowerShell variables for PHP: no name is better than a wrong one.
  if (found === "PHP" && !text.includes("<?php")) return "";
  return languageName(found);
}

/** The change that names the language of the block at pos, when it has none and one can be told. */
export function nameLanguage(state: EditorState, pos: number): TransactionSpec | null {
  const b = blockAt(state, pos);
  if (!b || b.node.getChild("CodeInfo")) return null;
  const mark = b.node.getChild("CodeMark");
  if (!mark) return null;
  const name = detectLanguage(state.sliceDoc(b.first.from, b.last.to));
  if (!name) return null;
  return { changes: { from: mark.to, insert: name }, userEvent: "input.language" };
}

/** After a paste into a code block with no language, names the language in its own undo step. */
const detectOnPaste = ViewPlugin.define((view) => ({
  update(u) {
    if (!u.transactions.some((tr) => tr.isUserEvent("input.paste"))) return;
    const head = u.state.selection.main.head;
    queueMicrotask(() => {
      if (view.state.selection.main.head !== head) return;
      const spec = nameLanguage(view.state, head);
      if (spec) view.dispatch(spec);
    });
  },
}));

// ---- The extension ----

export const blocks = [
  fenceFilter,
  detectOnPaste,
  Prec.high(
    keymap.of([
      { key: "Enter", run: listEnter, shift: listShiftEnter },
      { key: "Backspace", run: codeBackspace },
      { key: "Backspace", run: deleteMarkupBackward },
      { key: "Delete", run: codeDelete },
      { key: "Mod-a", run: selectBlock },
      { key: "ArrowUp", run: arrow(false) },
      { key: "ArrowDown", run: arrow(true) },
    ]),
  ),
];
