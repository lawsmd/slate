// Live preview: decorations that draw Markdown constructs in place.

import { ensureSyntaxTree, syntaxTree } from "@codemirror/language";
import { EditorState, Range, StateField } from "@codemirror/state";
import { Decoration, DecorationSet, EditorView } from "@codemirror/view";
import type { SyntaxNode } from "@lezer/common";
import { calloutPanels, calloutPattern, mentionScheme } from "./syntax";
import { focusField, renderCompartment, renderConfig } from "./config";
import {
  BulletWidget,
  CalloutWidget,
  CheckboxWidget,
  LanguageWidget,
  MentionWidget,
  RuleWidget,
  TableWidget,
} from "./widgets";

const hidden = Decoration.replace({});
const bullet = Decoration.replace({ widget: new BulletWidget() });
const rule = Decoration.replace({ widget: new RuleWidget() });

function build(state: EditorState): DecorationSet {
  const cfg = state.facet(renderConfig);
  const focused = state.field(focusField);
  const doc = state.doc;
  const tree = ensureSyntaxTree(state, doc.length, 200) ?? syntaxTree(state);
  const ranges = state.selection.ranges;
  const out: Range<Decoration>[] = [];

  /** Whether a cursor or selection reaches into [from, to]. */
  const touches = (from: number, to: number) => focused && ranges.some((r) => r.from <= to && r.to >= from);
  const linesTouched = (from: number, to: number) => touches(doc.lineAt(from).from, doc.lineAt(to).to);
  const text = (n: { from: number; to: number }) => doc.sliceString(n.from, n.to);
  const hide = (from: number, to: number) => {
    if (to > from) out.push(hidden.range(from, to));
  };
  const mark = (from: number, to: number, cls: string) => {
    if (to > from) out.push(Decoration.mark({ class: cls }).range(from, to));
  };
  const line = (pos: number, cls: string, attributes?: Record<string, string>) => {
    out.push(Decoration.line({ class: cls, attributes }).range(doc.lineAt(pos).from));
  };
  const hideMarks = (node: SyntaxNode, name: string) => {
    for (const m of node.getChildren(name)) hide(m.from, m.to);
  };
  const inline = (node: SyntaxNode, cls: string, markName: string) => {
    if (cfg.emphasis === "raw") return;
    mark(node.from, node.to, cls);
    if (!touches(node.from, node.to)) hideMarks(node, markName);
  };

  tree.iterate({
    enter(ref) {
      const node = ref.node;
      const name = ref.name;

      const atx = /^ATXHeading(\d)$/.exec(name);
      if (atx) {
        if (cfg.heading === "raw") return;
        line(node.from, `sl-h sl-h${atx[1]}`);
        if (!linesTouched(node.from, node.to)) {
          const m = node.getChild("HeaderMark");
          if (m) {
            let end = m.to;
            while (end < node.to && /[ \t]/.test(doc.sliceString(end, end + 1))) end++;
            hide(m.from, end);
          }
        } else {
          for (const m of node.getChildren("HeaderMark")) mark(m.from, m.to, "sl-dim");
        }
        return;
      }
      const setext = /^SetextHeading(\d)$/.exec(name);
      if (setext) {
        if (cfg.heading === "raw") return;
        const underline = node.getChild("HeaderMark");
        const last = underline ? doc.lineAt(underline.from).number - 1 : doc.lineAt(node.to).number;
        for (let n = doc.lineAt(node.from).number; n <= last; n++) line(doc.line(n).from, `sl-h sl-h${setext[1]}`);
        if (underline) mark(underline.from, underline.to, "sl-dim");
        return;
      }

      switch (name) {
        case "Emphasis":
          return inline(node, "sl-em", "EmphasisMark");
        case "StrongEmphasis":
          return inline(node, "sl-strong", "EmphasisMark");
        case "Strikethrough":
          return inline(node, "sl-strike", "StrikethroughMark");
        case "InlineCode":
          return inline(node, "sl-icode", "CodeMark");

        case "Link": {
          const url = node.getChild("URL");
          const marks = node.getChildren("LinkMark");
          if (!url || marks.length < 2) return;
          const touched = touches(node.from, node.to);
          if (text(url).startsWith(mentionScheme)) {
            if (cfg.mention === "raw") return false;
            if (touched) {
              mark(node.from, node.to, "sl-dim");
            } else {
              const label = doc.sliceString(marks[0].to, marks[1].from);
              out.push(Decoration.replace({ widget: new MentionWidget(label) }).range(node.from, node.to));
            }
            return false;
          }
          if (cfg.link === "raw") return;
          mark(marks[0].to, marks[1].from, "sl-link");
          if (touched) {
            mark(marks[1].from, node.to, "sl-dim");
          } else {
            hide(marks[0].from, marks[0].to);
            hide(marks[1].from, node.to);
          }
          return;
        }
        case "Autolink":
          if (cfg.link !== "raw") mark(node.from, node.to, "sl-link");
          return false;
        case "URL":
          if (cfg.link !== "raw" && node.parent?.name !== "Link" && node.parent?.name !== "Image") {
            mark(node.from, node.to, "sl-link");
          }
          return;

        case "ListItem": {
          if (cfg.list === "raw") return;
          const m = node.getChild("ListMark");
          if (!m) return;
          const task = node.getChild("Task");
          const marker = task?.getChild("TaskMarker");
          if (task && marker) {
            const checked = /x/i.test(text(marker));
            if (touches(m.from, marker.to)) {
              mark(m.from, marker.to, "sl-dim");
            } else {
              hide(m.from, marker.from);
              out.push(Decoration.replace({ widget: new CheckboxWidget(checked) }).range(marker.from, marker.to));
            }
            if (checked) mark(Math.min(marker.to + 1, task.to), task.to, "sl-task-done");
          } else if (node.parent?.name === "OrderedList") {
            mark(m.from, m.to, "sl-list-num");
          } else if (touches(m.from, m.to)) {
            mark(m.from, m.to, "sl-dim");
          } else {
            out.push(bullet.range(m.from, m.to));
          }
          return;
        }

        case "Blockquote": {
          if (cfg.quote === "raw") return;
          if (node.parent?.name === "Blockquote") return;
          const first = doc.lineAt(node.from);
          const last = doc.lineAt(node.to);
          const para = node.getChild("Paragraph");
          let callout: RegExpExecArray | null = null;
          if (para && cfg.callout !== "raw") {
            callout = calloutPattern.exec(doc.sliceString(para.from, Math.min(para.to, doc.lineAt(para.from).to)));
          }
          const panel = callout ? calloutPanels[callout[1].toLowerCase()] ?? "info" : "";
          for (let n = first.number; n <= last.number; n++) {
            let cls = callout ? `sl-callout sl-callout-${panel}` : "sl-quote";
            if (n === first.number) cls += " sl-block-first";
            if (n === last.number) cls += " sl-block-last";
            line(doc.line(n).from, cls);
          }
          if (callout && para) {
            const tagEnd = para.from + callout[0].length - callout[2].length;
            mark(tagEnd, para.from + callout[0].length, "sl-callout-title");
            if (linesTouched(para.from, para.from)) {
              mark(para.from, tagEnd, "sl-dim");
            } else {
              const widget = new CalloutWidget(callout[1], callout[2].trim() !== "");
              out.push(Decoration.replace({ widget }).range(para.from, tagEnd));
            }
          }
          return;
        }
        case "QuoteMark": {
          if (cfg.quote === "raw") return;
          if (linesTouched(node.from, node.to)) {
            mark(node.from, node.to, "sl-dim");
          } else {
            const end = doc.sliceString(node.to, node.to + 1) === " " ? node.to + 1 : node.to;
            hide(node.from, end);
          }
          return;
        }

        case "HorizontalRule":
          if (cfg.rule === "raw") return;
          if (linesTouched(node.from, node.to)) mark(node.from, node.to, "sl-dim");
          else out.push(rule.range(node.from, node.to));
          return;

        case "FencedCode": {
          if (cfg.code === "raw") return false;
          const marks = node.getChildren("CodeMark");
          const info = node.getChild("CodeInfo");
          const open = doc.lineAt(node.from);
          const end = doc.lineAt(node.to);
          const closed = marks.length >= 2 && doc.lineAt(marks[marks.length - 1].from).number > open.number;
          const inside = touches(open.from, end.to);
          // In live style both fences show while the cursor is anywhere in
          // the block. In block style the fences of a closed block never
          // show (blocks.ts keeps the cursor off them); an open fence with
          // no closing one shows while the cursor is on it.
          const showOpen = cfg.code === "live" ? inside : !closed && touches(open.from, open.to);
          const showClose = cfg.code === "live" && inside;
          for (let n = open.number; n <= end.number; n++) {
            const l = doc.line(n);
            const isOpen = n === open.number;
            const isClose = closed && n === end.number;
            let cls = "sl-code";
            let attributes: Record<string, string> | undefined;
            if (isOpen) cls += showOpen ? " sl-code-open" : " sl-code-open sl-code-head";
            else if (isClose) cls += showClose ? " sl-code-close" : " sl-code-close sl-code-foot";
            else attributes = { "data-ln": String(n - open.number) };
            if (n === end.number && !closed) cls += " sl-code-close";
            line(l.from, cls, attributes);
            if (isOpen && !showOpen && l.to > l.from) {
              const widget = new LanguageWidget(info ? text(info).trim() : "");
              out.push(Decoration.replace({ widget }).range(l.from, l.to));
            }
            if (isClose && !showClose) hide(l.from, l.to);
          }
          return false;
        }
        case "CodeBlock": {
          if (cfg.code === "raw") return false;
          const first = doc.lineAt(node.from).number;
          const last = doc.lineAt(node.to).number;
          for (let n = first; n <= last; n++) {
            let cls = "sl-code sl-code-plain";
            if (n === first) cls += " sl-code-open";
            if (n === last) cls += " sl-code-close";
            line(doc.line(n).from, cls);
          }
          return false;
        }

        case "Table": {
          if (cfg.table === "raw") return false;
          const first = doc.lineAt(node.from);
          const last = doc.lineAt(node.to);
          if (node.parent?.name === "Document" && !touches(first.from, last.to)) {
            const widget = new TableWidget(doc.sliceString(first.from, last.to));
            out.push(Decoration.replace({ widget, block: true }).range(first.from, last.to));
          } else {
            for (let n = first.number; n <= last.number; n++) line(doc.line(n).from, "sl-table-raw");
          }
          return false;
        }
      }
      return;
    },
  });

  return Decoration.set(out, true);
}

/** The decorations for the current document, selection and settings. */
export const liveField = StateField.define<DecorationSet>({
  create: build,
  update(value, tr) {
    const changed =
      tr.docChanged ||
      tr.selection !== undefined ||
      tr.reconfigured ||
      tr.startState.field(focusField) !== tr.state.field(focusField) ||
      syntaxTree(tr.startState) !== syntaxTree(tr.state);
    return changed ? build(tr.state) : value;
  },
  provide: (field) => EditorView.decorations.from(field),
});

export { renderCompartment };
