// The syntax tree as plain data, for a host that writes its own export.

import type { SyntaxNode } from "@lezer/common";
import type { Source } from "./targets";

/** One node of the syntax tree. */
export interface DocNode {
  /** The node's name in the Markdown grammar, such as "Paragraph" or "Emphasis". */
  type: string;
  /** Where the node starts and ends, in bytes of the UTF-8 Markdown. */
  from: number;
  to: number;
  children?: DocNode[];
}

export interface Doc {
  markdown: string;
  root: DocNode;
}

/**
 * Converts a parsed document. Offsets are counted in UTF-8 bytes, which is
 * how a Go host indexes the Markdown string.
 */
export function toDoc(source: Source): Doc {
  const { text } = source;
  // bytes[i] is the UTF-8 length of the first i UTF-16 units.
  const bytes = new Uint32Array(text.length + 1);
  for (let i = 0; i < text.length; i++) {
    const unit = text.charCodeAt(i);
    // A surrogate pair is four bytes: two are counted on each half.
    const size = unit < 0x80 ? 1 : unit < 0x800 ? 2 : unit >= 0xd800 && unit <= 0xdfff ? 2 : 3;
    bytes[i + 1] = bytes[i] + size;
  }
  const convert = (n: SyntaxNode): DocNode => {
    const node: DocNode = { type: n.name, from: bytes[n.from], to: bytes[n.to] };
    for (let c = n.firstChild; c; c = c.nextSibling) (node.children ??= []).push(convert(c));
    return node;
  };
  return { markdown: text, root: convert(source.tree.topNode) };
}
