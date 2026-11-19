// Conventions slate reads on top of Markdown. The live view draws them and
// the targets export them.

/** Obsidian callout types and the kind each one is drawn and exported as. */
export const calloutPanels: Record<string, string> = {
  info: "info", todo: "info", abstract: "info", summary: "info", tldr: "info",
  question: "info", help: "info", faq: "info", example: "info",
  note: "note", quote: "note", cite: "note",
  tip: "success", hint: "success", important: "success", success: "success", check: "success", done: "success",
  warning: "warning", caution: "warning", attention: "warning",
  danger: "error", error: "error", bug: "error", failure: "error", fail: "error", missing: "error",
};

/** The first line of a callout: `[!type] title`. */
export const calloutPattern = /^\[!(\w+)\][+-]?[ \t]*(.*)$/;

/** The scheme of a mention link: `[@Name](mention:accountId)`. */
export const mentionScheme = "mention:";
