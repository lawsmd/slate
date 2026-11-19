// A target is a place the document goes after it is written: a Jira comment,
// an Obsidian note, a Markdown file. It owns the syntax the editor accepts,
// how that syntax is drawn while writing, and the export.
//
// The editor parses with the target's `syntax`, and `parse` here builds the
// same parser for export. What is exported is what was shown.
//
// To add a target, write one file that exports a Target and add it to
// `targets` below.

import type { Tree } from "@lezer/common";
import { parser as commonmark, type MarkdownExtension } from "@lezer/markdown";
import type { RenderConfig } from "../config";
import { jira } from "./jira";
import { markdown } from "./markdown";

/** A document and its syntax tree, parsed with a target's syntax. */
export interface Source {
  text: string;
  tree: Tree;
}

/** A document in the form its destination takes. */
export interface Exported {
  /** The media type of `body`. */
  mime: string;
  body: string;
}

/** A stretch of the document the target cannot carry as written. */
export interface Loss {
  from: number;
  to: number;
  /** What happens to it on export, shown to the writer. */
  reason: string;
}

export interface Target {
  name: string;
  /** Markdown extensions beyond CommonMark. The editor and the export both parse with them. */
  syntax: MarkdownExtension[];
  /** Styles for the live view. Missing keys keep the editor's defaults. */
  render?: Partial<RenderConfig>;
  /** Finds what the export changes or drops. The editor marks each stretch. */
  unsupported?(source: Source): Loss[];
  export(source: Source): Exported;
  /** Turns the destination's form back into Markdown, where the target can. */
  import?(body: string): string;
}

export const targets: Record<string, Target> = { markdown, jira };

export const defaultTarget = "markdown";

/** Returns the target of that name. Throws for a name no target has. */
export function target(name: string): Target {
  const t = targets[name];
  if (!t) throw new Error(`no target named "${name}"; there are: ${Object.keys(targets).join(", ")}`);
  return t;
}

/** Parses text the way the editor does while it shows that target. */
export function parse(t: Target, text: string): Source {
  return { text, tree: commonmark.configure(t.syntax).parse(text) };
}
