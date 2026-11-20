// Atlassian Document Format to Markdown, for a comment or description
// fetched from Jira to be edited. It writes the Markdown that jira.ts
// exports as the same ADF.
//
// ADF holds things Markdown has no form for. An attached image or file
// (media) is left out, text colour and underline are dropped, and a date, a
// status or an emoji is kept as its text. fromADF reports what it left out
// through `lost`, so a host can warn before it sends the edited text back
// over the original.

import { mentionScheme } from "../syntax";
import type { ADFNode, Mark } from "./jira";

/** Converts an ADF document, as JSON text or a parsed node, to Markdown. */
export function fromADF(body: string | ADFNode, lost?: Set<string>): string {
  const doc: ADFNode = typeof body === "string" ? JSON.parse(body) : body;
  if (!doc || doc.type !== "doc") throw new Error("this is not an ADF document: its type is not \"doc\"");
  return blocks(doc.content ?? [], lost ?? new Set());
}

/** The node types in an ADF document that fromADF leaves out. */
export function lostOnImport(body: string | ADFNode): string[] {
  const lost = new Set<string>();
  fromADF(body, lost);
  return [...lost].sort();
}

function blocks(nodes: ADFNode[], lost: Set<string>): string {
  const out: string[] = [];
  for (const n of nodes) {
    const text = block(n, lost);
    if (text !== "") out.push(text);
  }
  return out.join("\n\n");
}

function block(n: ADFNode, lost: Set<string>): string {
  const content = n.content ?? [];
  switch (n.type) {
    case "paragraph":
      return startOfLine(inline(content, lost));
    case "heading": {
      const level = Math.min(Math.max(Number(n.attrs?.level) || 1, 1), 6);
      return "#".repeat(level) + " " + inline(content, lost).replace(/\n/g, " ");
    }
    case "blockquote":
      return prefix(blocks(content, lost), "> ", "> ");
    case "panel":
      return panel(n, lost);
    case "bulletList":
      return content.map((item) => listItem(item, "- ", lost)).join("\n");
    case "orderedList": {
      const start = Number(n.attrs?.order) || 1;
      return content.map((item, i) => listItem(item, `${start + i}. `, lost)).join("\n");
    }
    case "taskList":
      return taskList(n, lost);
    case "codeBlock":
      return code(n);
    case "rule":
      return "---";
    case "table":
      return table(n, lost);
    case "mediaSingle":
    case "mediaGroup":
    case "media":
      lost.add("media");
      return "";
    default:
      // A container this file does not know, such as "expand": keep what
      // is inside it.
      if (content.length && content.every((c) => c.type !== "text" && !inlineTypes.has(c.type))) {
        return blocks(content, lost);
      }
      if (content.length) return startOfLine(inline(content, lost));
      lost.add(n.type);
      return "";
  }
}

const inlineTypes = new Set(["text", "hardBreak", "mention", "emoji", "inlineCard", "date", "status"]);

/** Puts `first` before the first line and `rest` before the others. */
function prefix(text: string, first: string, rest: string): string {
  return text
    .split("\n")
    .map((line, i) => ((i ? rest : first) + line).trimEnd())
    .join("\n");
}

function panel(n: ADFNode, lost: Set<string>): string {
  const content = [...(n.content ?? [])];
  let head = `[!${String(n.attrs?.panelType ?? "info")}]`;
  // The export writes a callout's title as a first paragraph in bold.
  const first = content[0];
  const only = first?.type === "paragraph" && first.content?.length === 1 ? first.content[0] : undefined;
  if (only?.type === "text" && only.marks?.length === 1 && only.marks[0].type === "strong" && content.length > 1) {
    head += " " + escape(only.text ?? "");
    content.shift();
  }
  const body = blocks(content, lost);
  return prefix(body ? head + "\n" + body : head, "> ", "> ");
}

function listItem(item: ADFNode, marker: string, lost: Set<string>): string {
  const indent = " ".repeat(marker.length);
  const parts: string[] = [];
  let text = "";
  for (const child of item.content ?? []) {
    const b = block(child, lost);
    if (b === "") continue;
    const list = child.type === "bulletList" || child.type === "orderedList" || child.type === "taskList";
    // A list nested straight under the item's paragraph needs no blank line.
    text += (parts.length ? (list && parts.length === 1 ? "\n" : "\n\n") : "") + b;
    parts.push(b);
  }
  return prefix(text, marker, indent);
}

function taskList(n: ADFNode, lost: Set<string>): string {
  const out: string[] = [];
  for (const item of n.content ?? []) {
    if (item.type === "taskList") {
      out.push(prefix(taskList(item, lost), "  ", "  "));
      continue;
    }
    const box = item.attrs?.state === "DONE" ? "- [x] " : "- [ ] ";
    out.push(prefix(inline(item.content ?? [], lost), box, "  "));
  }
  return out.join("\n");
}

function code(n: ADFNode): string {
  const text = (n.content ?? []).map((c) => c.text ?? "").join("");
  // The fence is longer than any run of backticks in the code.
  const longest = Math.max(0, ...(text.match(/`+/g) ?? []).map((run) => run.length));
  const fence = "`".repeat(Math.max(3, longest + 1));
  const language = typeof n.attrs?.language === "string" ? n.attrs.language : "";
  return fence + language + "\n" + text + (text === "" || text.endsWith("\n") ? "" : "\n") + fence;
}

function table(n: ADFNode, lost: Set<string>): string {
  const rows = (n.content ?? []).filter((row) => row.type === "tableRow");
  if (!rows.length) return "";
  const cell = (c: ADFNode) =>
    (c.content ?? [])
      .map((b) => block(b, lost))
      .filter((s) => s !== "")
      .join(" ")
      .replace(/\n+/g, " ")
      .replace(/(?<!\\)\|/g, "\\|"); // a bar inside code is not yet escaped
  const lines = rows.map((row) => "| " + (row.content ?? []).map(cell).join(" | ") + " |");
  const columns = Math.max(...rows.map((row) => row.content?.length ?? 0));
  // A Markdown table has a header row. The first row is taken for it.
  lines.splice(1, 0, "|" + " --- |".repeat(columns));
  return lines.join("\n");
}

/** A piece of a line: text with marks, or Markdown that is already written. */
interface Piece {
  text: string;
  marks: Mark[];
  done?: boolean;
}

// Marks from the outermost to the innermost, as they are written.
const nesting = ["link", "strong", "em", "strike"];
const wrappers: Record<string, (inner: string, mark: Mark) => string> = {
  link: (inner, mark) => `[${inner}](${String(mark.attrs?.href ?? "")})`,
  strong: (inner) => `**${inner}**`,
  em: (inner) => `*${inner}*`,
  strike: (inner) => `~~${inner}~~`,
};

function inline(nodes: ADFNode[], lost: Set<string>): string {
  const pieces: Piece[] = [];
  const done = (text: string) => pieces.push({ text, marks: [], done: true });
  for (const n of nodes) {
    switch (n.type) {
      case "text":
        pieces.push({ text: n.text ?? "", marks: n.marks ?? [] });
        break;
      case "hardBreak":
        done("\n");
        break;
      case "mention": {
        const id = String(n.attrs?.id ?? "");
        const name = String(n.attrs?.text ?? "").replace(/^@/, "") || id;
        done(`[@${escape(name)}](${mentionScheme}${encodeURIComponent(id)})`);
        break;
      }
      case "emoji":
        done(escape(String(n.attrs?.text ?? n.attrs?.shortName ?? "")));
        break;
      case "inlineCard": {
        const url = String(n.attrs?.url ?? "");
        if (url) done(`[${escape(url)}](${url})`);
        break;
      }
      case "date": {
        const at = Number(n.attrs?.timestamp);
        if (Number.isFinite(at)) done(new Date(at).toISOString().slice(0, 10));
        break;
      }
      case "status":
        done(escape(String(n.attrs?.text ?? "")));
        break;
      default:
        if (n.content?.length) done(inline(n.content, lost));
        else lost.add(n.type);
    }
  }
  return write(pieces, 0);
}

/**
 * Writes pieces with the marks from nesting[level] on. Neighbours that share
 * a mark are written inside one pair of its delimiters.
 */
function write(pieces: Piece[], level: number): string {
  if (level >= nesting.length) return pieces.map(leaf).join("");
  const type = nesting[level];
  const markOf = (p: Piece) => (p.done ? undefined : p.marks.find((m) => m.type === type));
  let out = "";
  for (let i = 0; i < pieces.length; ) {
    const mark = markOf(pieces[i]);
    let end = i + 1;
    while (end < pieces.length && same(markOf(pieces[end]), mark)) end++;
    const inner = write(pieces.slice(i, end), level + 1);
    i = end;
    if (!mark) {
      out += inner;
      continue;
    }
    // White space at the ends stays outside the delimiters, which would
    // not close after a space.
    const m = /^(\s*)([\s\S]*?)(\s*)$/.exec(inner)!;
    out += m[2] === "" ? inner : m[1] + wrappers[type](m[2], mark) + m[3];
  }
  return out;
}

function same(a: Mark | undefined, b: Mark | undefined): boolean {
  return JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
}

function leaf(p: Piece): string {
  if (p.done) return p.text;
  if (!p.marks.some((m) => m.type === "code")) return escape(p.text);
  const longest = Math.max(0, ...(p.text.match(/`+/g) ?? []).map((run) => run.length));
  const ticks = "`".repeat(longest + 1);
  const pad = p.text.startsWith("`") || p.text.endsWith("`") ? " " : "";
  return ticks + pad + p.text + pad + ticks;
}

/** Escapes the characters Markdown would read as syntax inside a line. */
function escape(text: string): string {
  return text
    .replace(/[\\`*[\]<~|]/g, "\\$&")
    // An underscore inside a word is plain text already.
    .replace(/(?<![A-Za-z0-9])_|_(?![A-Za-z0-9])/g, "\\_");
}

/** Escapes what would make a paragraph's line a heading, a quote or a list. */
function startOfLine(text: string): string {
  return text
    .split("\n")
    .map((line) => line.replace(/^(\s*)(#{1,6}(?=\s|$)|>|[-+](?=\s|$)|(\d+)([.)])(?=\s|$))/, (_, sp, what, num, dot) =>
      num !== undefined ? `${sp}${num}\\${dot}` : `${sp}\\${what}`,
    ))
    .join("\n");
}
