import { EditorView, WidgetType } from "@codemirror/view";
import { toADF, type ADFNode } from "./adf";
import { openLanguagePicker } from "./langpicker";

export class BulletWidget extends WidgetType {
  eq(): boolean {
    return true;
  }
  toDOM(): HTMLElement {
    const el = document.createElement("span");
    el.className = "sl-bullet";
    el.textContent = "•";
    return el;
  }
}

export class RuleWidget extends WidgetType {
  eq(): boolean {
    return true;
  }
  toDOM(): HTMLElement {
    const el = document.createElement("span");
    el.className = "sl-hr";
    return el;
  }
}

export class CheckboxWidget extends WidgetType {
  constructor(readonly checked: boolean) {
    super();
  }
  eq(other: CheckboxWidget): boolean {
    return other.checked === this.checked;
  }
  toDOM(view: EditorView): HTMLElement {
    const el = document.createElement("span");
    el.className = "sl-check" + (this.checked ? " sl-check-on" : "");
    el.textContent = this.checked ? "✓" : "";
    el.addEventListener("mousedown", (e) => {
      e.preventDefault();
      const pos = view.posAtDOM(el);
      if (/^\[[ xX]\]$/.test(view.state.sliceDoc(pos, pos + 3))) {
        view.dispatch({ changes: { from: pos + 1, to: pos + 2, insert: this.checked ? " " : "x" } });
      }
    });
    return el;
  }
  ignoreEvent(): boolean {
    return true;
  }
}

export class MentionWidget extends WidgetType {
  constructor(readonly label: string) {
    super();
  }
  eq(other: MentionWidget): boolean {
    return other.label === this.label;
  }
  toDOM(): HTMLElement {
    const el = document.createElement("span");
    el.className = "sl-mention";
    el.textContent = this.label;
    return el;
  }
}

export class CalloutWidget extends WidgetType {
  constructor(readonly kind: string, readonly withTitle: boolean) {
    super();
  }
  eq(other: CalloutWidget): boolean {
    return other.kind === this.kind && other.withTitle === this.withTitle;
  }
  toDOM(): HTMLElement {
    const el = document.createElement("span");
    el.className = "sl-callout-label";
    const name = this.kind.charAt(0).toUpperCase() + this.kind.slice(1).toLowerCase();
    el.textContent = this.withTitle ? "● " : "● " + name;
    return el;
  }
}

/** The header of a code block: its language, which opens the picker. */
export class LanguageWidget extends WidgetType {
  constructor(readonly language: string) {
    super();
  }
  eq(other: LanguageWidget): boolean {
    return other.language === this.language;
  }
  toDOM(view: EditorView): HTMLElement {
    const el = document.createElement("span");
    el.className = "sl-lang";
    el.textContent = (this.language || "plain text") + " ▾";
    el.addEventListener("mousedown", (e) => {
      e.preventDefault();
      openLanguagePicker(view, view.posAtDOM(el));
    });
    return el;
  }
  ignoreEvent(): boolean {
    return true;
  }
}

/** A table drawn as a grid. A click puts the cursor in its source text. */
export class TableWidget extends WidgetType {
  constructor(readonly source: string) {
    super();
  }
  eq(other: TableWidget): boolean {
    return other.source === this.source;
  }
  toDOM(view: EditorView): HTMLElement {
    const wrap = document.createElement("div");
    wrap.className = "sl-table-wrap";
    const table = document.createElement("table");
    table.className = "sl-table";
    const node = toADF(this.source).content?.[0];
    for (const row of node?.type === "table" ? node.content ?? [] : []) {
      const tr = table.insertRow();
      for (const cell of row.content ?? []) {
        const el = document.createElement(cell.type === "tableHeader" ? "th" : "td");
        el.append(inlineDOM(cell.content?.[0]?.content ?? []));
        tr.append(el);
      }
    }
    wrap.append(table);
    wrap.addEventListener("mousedown", (e) => {
      e.preventDefault();
      view.dispatch({ selection: { anchor: view.posAtDOM(wrap) } });
      view.focus();
    });
    return wrap;
  }
  ignoreEvent(): boolean {
    return true;
  }
}

const markTags: Record<string, string> = { strong: "strong", em: "em", strike: "s", code: "code", link: "span" };

/** Draws exported inline nodes, for content shown outside the editable text. */
export function inlineDOM(nodes: ADFNode[]): DocumentFragment {
  const out = document.createDocumentFragment();
  for (const node of nodes) {
    if (node.type === "hardBreak") {
      out.append(document.createElement("br"));
    } else if (node.type === "mention") {
      const el = document.createElement("span");
      el.className = "sl-mention";
      el.textContent = String(node.attrs?.text ?? "");
      out.append(el);
    } else if (node.type === "text") {
      let el: Node = document.createTextNode(node.text ?? "");
      for (const mark of node.marks ?? []) {
        const wrapper = document.createElement(markTags[mark.type] ?? "span");
        if (mark.type === "link") wrapper.className = "sl-link";
        if (mark.type === "code") wrapper.className = "sl-icode";
        wrapper.append(el);
        el = wrapper;
      }
      out.append(el);
    }
  }
  return out;
}
