// Markdown to Atlassian Document Format.
//
// The conversion reads the same Lezer syntax tree the editor draws from, so
// what is exported is what was shown.

import { parser as baseParser, GFM } from "@lezer/markdown";
import type { SyntaxNode } from "@lezer/common";

export const mdParser = baseParser.configure([GFM]);

export interface Mark {
  type: string;
  attrs?: Record<string, unknown>;
}

export interface ADFNode {
  type: string;
  version?: number;
  attrs?: Record<string, unknown>;
  content?: ADFNode[];
  text?: string;
  marks?: Mark[];
}

/** Obsidian callout types and the Jira panel each one maps to. */
export const calloutPanels: Record<string, string> = {
  info: "info", todo: "info", abstract: "info", summary: "info", tldr: "info",
  question: "info", help: "info", faq: "info", example: "info",
  note: "note", quote: "note", cite: "note",
  tip: "success", hint: "success", important: "success", success: "success", check: "success", done: "success",
  warning: "warning", caution: "warning", attention: "warning",
  danger: "error", error: "error", bug: "error", failure: "error", fail: "error", missing: "error",
};

export const calloutPattern = /^\[!(\w+)\][+-]?[ \t]*(.*)$/;

/** The scheme of a mention link: `[@Name](mention:accountId)`. */
export const mentionScheme = "mention:";

const entities: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " " };

// Block types Jira accepts inside each container.
const inListItem = new Set(["paragraph", "bulletList", "orderedList", "codeBlock", "taskList"]);
const inQuote = new Set(["paragraph", "bulletList", "orderedList", "codeBlock"]);
const inPanel = new Set(["paragraph", "heading", "bulletList", "orderedList", "codeBlock", "taskList", "rule"]);

export function toADF(doc: string): ADFNode {
  return new Converter(doc).document();
}

class Converter {
  private ids = 0;

  constructor(private readonly doc: string) {}

  document(): ADFNode {
    const tree = mdParser.parse(this.doc);
    return { version: 1, type: "doc", content: this.blocks(tree.topNode) };
  }

  private slice(from: number, to: number): string {
    return this.doc.slice(from, to);
  }

  private nextId(): string {
    return `slate-${++this.ids}`;
  }

  private blocks(parent: SyntaxNode, skip?: Set<string>): ADFNode[] {
    const out: ADFNode[] = [];
    for (let n = parent.firstChild; n; n = n.nextSibling) {
      if (!skip?.has(n.name)) out.push(...this.block(n));
    }
    return out;
  }

  private block(n: SyntaxNode): ADFNode[] {
    const heading = /^(?:ATX|Setext)Heading(\d)$/.exec(n.name);
    if (heading) {
      return [{ type: "heading", attrs: { level: Number(heading[1]) }, content: trim(this.inline(n)) }];
    }
    switch (n.name) {
      case "Paragraph":
        return [paragraph(trim(this.inline(n)))];
      case "Blockquote":
        return [this.quote(n)];
      case "BulletList":
        return [this.list(n, false)];
      case "OrderedList":
        return [this.list(n, true)];
      case "FencedCode":
      case "CodeBlock":
        return [this.code(n)];
      case "HorizontalRule":
        return [{ type: "rule" }];
      case "Table":
        return [this.table(n)];
      case "QuoteMark":
      case "ListMark":
        return [];
      default:
        return [this.plain(n)];
    }
  }

  /** A paragraph holding a node's source text, for blocks with no ADF form here. */
  private plain(n: SyntaxNode): ADFNode {
    const out: ADFNode[] = [];
    this.text(out, n.from, n.to, []);
    return paragraph(trim(out));
  }

  /** Replaces blocks the container does not accept with their source text. */
  private restrict(n: SyntaxNode, allowed: Set<string>, skip: Set<string>): ADFNode[] {
    const out: ADFNode[] = [];
    for (let c = n.firstChild; c; c = c.nextSibling) {
      if (skip.has(c.name)) continue;
      for (const b of this.block(c)) out.push(allowed.has(b.type) ? b : this.plain(c));
    }
    return out;
  }

  private quote(n: SyntaxNode): ADFNode {
    const first = n.getChild("Paragraph");
    if (first) {
      const firstLine = this.slice(first.from, first.to).split("\n", 1)[0];
      const m = calloutPattern.exec(firstLine);
      if (m) {
        const content: ADFNode[] = [];
        const title = m[2].trim();
        if (title) content.push(paragraph([{ type: "text", text: title, marks: [{ type: "strong" }] }]));
        const rest = trim(this.inline(first, [], first.from + firstLine.length + 1, first.to));
        if (rest.length) content.push(paragraph(rest));
        for (let c = first.nextSibling; c; c = c.nextSibling) {
          if (c.name === "QuoteMark") continue;
          for (const b of this.block(c)) content.push(inPanel.has(b.type) ? b : this.plain(c));
        }
        if (!content.length) content.push(paragraph([]));
        const panelType = calloutPanels[m[1].toLowerCase()] ?? "info";
        return { type: "panel", attrs: { panelType }, content };
      }
    }
    const content = this.restrict(n, inQuote, new Set(["QuoteMark"]));
    return { type: "blockquote", content: content.length ? content : [paragraph([])] };
  }

  private list(n: SyntaxNode, ordered: boolean): ADFNode {
    const items = n.getChildren("ListItem");
    if (!ordered && items.length > 0 && items.every((it) => it.getChild("Task"))) {
      return this.taskList(items);
    }
    const node: ADFNode = {
      type: ordered ? "orderedList" : "bulletList",
      content: items.map((it) => this.listItem(it)),
    };
    if (ordered && items.length) {
      const mark = items[0].getChild("ListMark");
      const start = mark ? parseInt(this.slice(mark.from, mark.to), 10) : 1;
      if (Number.isFinite(start) && start !== 1) node.attrs = { order: start };
    }
    return node;
  }

  private listItem(item: SyntaxNode): ADFNode {
    const content: ADFNode[] = [];
    for (let c = item.firstChild; c; c = c.nextSibling) {
      if (c.name === "ListMark" || c.name === "QuoteMark") continue;
      if (c.name === "Task") {
        // A task in a list that also has plain items keeps its marker as text.
        const marker = c.getChild("TaskMarker");
        const inline: ADFNode[] = [];
        if (marker) this.text(inline, marker.from, marker.to, []);
        this.inlineInto(inline, c, [], marker ? marker.to : c.from, c.to);
        content.push(paragraph(trim(inline)));
        continue;
      }
      for (const b of this.block(c)) content.push(inListItem.has(b.type) ? b : this.plain(c));
    }
    // Jira requires an item to open with a paragraph or a code block.
    if (!content.length || (content[0].type !== "paragraph" && content[0].type !== "codeBlock")) {
      content.unshift(paragraph([]));
    }
    return { type: "listItem", content };
  }

  private taskList(items: SyntaxNode[]): ADFNode {
    const content: ADFNode[] = [];
    for (const item of items) {
      const task = item.getChild("Task")!;
      const marker = task.getChild("TaskMarker");
      const done = marker ? /x/i.test(this.slice(marker.from, marker.to)) : false;
      const inline = trim(this.inline(task, [], marker ? marker.to : task.from, task.to));
      const nested: ADFNode[] = [];
      for (let c = task.nextSibling; c; c = c.nextSibling) {
        if (c.name === "ListMark" || c.name === "QuoteMark") continue;
        const blocks = this.block(c);
        if (blocks.length === 1 && blocks[0].type === "taskList") {
          nested.push(blocks[0]);
        } else {
          // A task item holds inline content only. Other blocks follow as text.
          inline.push({ type: "hardBreak" });
          this.text(inline, c.from, c.to, []);
        }
      }
      content.push({
        type: "taskItem",
        attrs: { localId: this.nextId(), state: done ? "DONE" : "TODO" },
        content: trim(inline),
      });
      content.push(...nested);
    }
    return { type: "taskList", attrs: { localId: this.nextId() }, content };
  }

  private code(n: SyntaxNode): ADFNode {
    const node: ADFNode = { type: "codeBlock" };
    const info = n.getChild("CodeInfo");
    const language = info ? this.slice(info.from, info.to).trim().split(/\s+/)[0] : "";
    if (language) node.attrs = { language };
    const parts = n.getChildren("CodeText");
    if (parts.length) {
      let text = this.slice(parts[0].from, parts[parts.length - 1].to);
      if (n.name === "CodeBlock") text = text.replace(/\n(?: {4}|\t)/g, "\n");
      // Inside a quote or list the continuation lines carry the container's prefix.
      const column = parts[0].from - this.doc.lastIndexOf("\n", parts[0].from - 1) - 1;
      if (column > 0 && n.name === "FencedCode") {
        const prefix = new RegExp(`\\n[ \\t>]{0,${column}}`, "g");
        text = text.replace(prefix, "\n");
      }
      if (text) node.content = [{ type: "text", text }];
    }
    return node;
  }

  private table(n: SyntaxNode): ADFNode {
    const rows: ADFNode[] = [];
    let columns = 0;
    for (let row = n.firstChild; row; row = row.nextSibling) {
      if (row.name !== "TableHeader" && row.name !== "TableRow") continue;
      const header = row.name === "TableHeader";
      const cells = this.cells(row);
      if (header) columns = cells.length;
      while (cells.length < columns) cells.push([]);
      if (columns) cells.length = columns;
      rows.push({
        type: "tableRow",
        content: cells.map((content) => ({
          type: header ? "tableHeader" : "tableCell",
          attrs: {},
          content: [paragraph(content)],
        })),
      });
    }
    return { type: "table", attrs: { isNumberColumnEnabled: false, layout: "default" }, content: rows };
  }

  /** The inline content of each cell in a table row, empty cells included. */
  private cells(row: SyntaxNode): ADFNode[][] {
    // The parser makes no node for an empty cell, so cells are found between
    // the delimiters.
    const bounds: [number, number][] = [];
    let start = row.from;
    for (let c = row.firstChild; c; c = c.nextSibling) {
      if (c.name !== "TableDelimiter") continue;
      bounds.push([start, c.from]);
      start = c.to;
    }
    bounds.push([start, row.to]);
    const blank = ([from, to]: [number, number]) => !this.slice(from, to).trim();
    if (bounds.length > 1 && blank(bounds[0])) bounds.shift();
    if (bounds.length > 1 && blank(bounds[bounds.length - 1])) bounds.pop();
    return bounds.map(([from, to]) => {
      for (let c = row.firstChild; c; c = c.nextSibling) {
        if (c.name === "TableCell" && c.from >= from && c.to <= to) return trim(this.inline(c));
      }
      return [];
    });
  }

  private inline(n: SyntaxNode, marks: Mark[] = [], from = n.from, to = n.to): ADFNode[] {
    const out: ADFNode[] = [];
    this.inlineInto(out, n, marks, from, to);
    return out;
  }

  private inlineInto(out: ADFNode[], n: SyntaxNode, marks: Mark[], from: number, to: number): void {
    let pos = from;
    for (let c = n.firstChild; c; c = c.nextSibling) {
      if (c.from < from || c.to > to) continue;
      this.text(out, pos, c.from, marks);
      this.inlineNode(out, c, marks);
      pos = c.to;
    }
    this.text(out, pos, to, marks);
  }

  private inlineNode(out: ADFNode[], n: SyntaxNode, marks: Mark[]): void {
    switch (n.name) {
      case "Emphasis":
        return this.inlineInto(out, n, withMark(marks, { type: "em" }), n.from, n.to);
      case "StrongEmphasis":
        return this.inlineInto(out, n, withMark(marks, { type: "strong" }), n.from, n.to);
      case "Strikethrough":
        return this.inlineInto(out, n, withMark(marks, { type: "strike" }), n.from, n.to);
      case "InlineCode": {
        const codeMarks = n.getChildren("CodeMark");
        if (codeMarks.length < 2) return pushText(out, this.slice(n.from, n.to), marks);
        let code = this.slice(codeMarks[0].to, codeMarks[codeMarks.length - 1].from).replace(/\n/g, " ");
        if (code.length > 2 && code.startsWith(" ") && code.endsWith(" ") && code.trim()) code = code.slice(1, -1);
        // Jira allows the code mark together with a link and nothing else.
        const link = marks.filter((m) => m.type === "link");
        return pushText(out, code, [{ type: "code" }, ...link]);
      }
      case "Link":
        return this.link(out, n, marks);
      case "Image": {
        const url = n.getChild("URL");
        const linkMarks = n.getChildren("LinkMark");
        if (!url || linkMarks.length < 2) return pushText(out, this.slice(n.from, n.to), marks);
        const href = this.slice(url.from, url.to);
        const alt = this.slice(linkMarks[0].to, linkMarks[1].from) || href;
        return pushText(out, alt, withMark(marks, { type: "link", attrs: { href } }));
      }
      case "Autolink": {
        const url = n.getChild("URL");
        const href = url ? this.slice(url.from, url.to) : this.slice(n.from + 1, n.to - 1);
        return pushText(out, href, withMark(marks, { type: "link", attrs: { href: absolute(href) } }));
      }
      case "URL": {
        const href = this.slice(n.from, n.to);
        return pushText(out, href, withMark(marks, { type: "link", attrs: { href: absolute(href) } }));
      }
      case "HardBreak":
        if (out.length && out[out.length - 1].type !== "hardBreak") out.push({ type: "hardBreak" });
        return;
      case "Escape":
        return pushText(out, this.slice(n.from + 1, n.to), marks);
      case "Entity": {
        const raw = this.slice(n.from, n.to);
        const name = raw.slice(1, -1);
        let decoded = entities[name];
        if (decoded === undefined && name.startsWith("#")) {
          const code = /^#x/i.test(name) ? parseInt(name.slice(2), 16) : parseInt(name.slice(1), 10);
          if (Number.isFinite(code) && code > 0 && code <= 0x10ffff) decoded = String.fromCodePoint(code);
        }
        return pushText(out, decoded ?? raw, marks);
      }
      case "QuoteMark":
      case "ListMark":
      case "HeaderMark":
      case "TaskMarker":
      case "EmphasisMark":
      case "StrikethroughMark":
      case "CodeMark":
      case "LinkMark":
      case "TableDelimiter":
        return;
      default:
        return this.text(out, n.from, n.to, marks);
    }
  }

  private link(out: ADFNode[], n: SyntaxNode, marks: Mark[]): void {
    const url = n.getChild("URL");
    const linkMarks = n.getChildren("LinkMark");
    if (!url || linkMarks.length < 2) {
      // A reference link or a bare [bracket]: keep the text as written.
      this.text(out, n.from, n.to, marks);
      return;
    }
    const href = this.slice(url.from, url.to);
    const from = linkMarks[0].to;
    const to = linkMarks[1].from;
    if (href.startsWith(mentionScheme)) {
      let id = href.slice(mentionScheme.length);
      try {
        id = decodeURIComponent(id);
      } catch {
        // keep the raw text
      }
      const label = this.slice(from, to);
      out.push({ type: "mention", attrs: { id, text: label.startsWith("@") ? label : "@" + label } });
      return;
    }
    this.inlineInto(out, n, withMark(marks, { type: "link", attrs: { href } }), from, to);
  }

  /**
   * Appends source text. A line break inside a paragraph becomes a hard
   * break, because the editor shows it as a new line.
   */
  private text(out: ADFNode[], from: number, to: number, marks: Mark[]): void {
    if (to <= from) return;
    const lines = this.slice(from, to).split("\n");
    lines.forEach((line, i) => {
      if (i > 0 && out.length && out[out.length - 1].type !== "hardBreak") out.push({ type: "hardBreak" });
      if (i < lines.length - 1) line = line.trimEnd();
      pushText(out, line, marks);
    });
  }
}

function paragraph(content: ADFNode[]): ADFNode {
  return content.length ? { type: "paragraph", content } : { type: "paragraph" };
}

function absolute(href: string): string {
  if (/^[a-z][\w+.-]*:/i.test(href)) return href;
  if (/^[^\s@/]+@[^\s@/]+$/.test(href)) return "mailto:" + href;
  return "http://" + href;
}

function withMark(marks: Mark[], mark: Mark): Mark[] {
  return marks.some((m) => m.type === mark.type) ? marks : [...marks, mark];
}

function sameMarks(a: Mark[] | undefined, b: Mark[]): boolean {
  return JSON.stringify(a ?? []) === JSON.stringify(b);
}

function pushText(out: ADFNode[], text: string, marks: Mark[]): void {
  const last = out[out.length - 1];
  // Text at the start of a line loses its indentation and any space left
  // behind by a container prefix such as "> ".
  if (!last || last.type === "hardBreak") text = text.trimStart();
  if (!text) return;
  if (last && last.type === "text" && sameMarks(last.marks, marks)) {
    last.text += text;
    return;
  }
  const node: ADFNode = { type: "text", text };
  if (marks.length) node.marks = marks;
  out.push(node);
}

/** Removes hard breaks and white space from both ends of inline content. */
function trim(nodes: ADFNode[]): ADFNode[] {
  while (nodes.length && nodes[nodes.length - 1].type === "hardBreak") nodes.pop();
  while (nodes.length && nodes[0].type === "hardBreak") nodes.shift();
  const last = nodes[nodes.length - 1];
  if (last?.type === "text") {
    last.text = last.text!.trimEnd();
    if (!last.text) {
      nodes.pop();
      return trim(nodes);
    }
  }
  return nodes;
}
