import { autocompletion, completionKeymap } from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { commonmarkLanguage, markdown } from "@codemirror/lang-markdown";
import { HighlightStyle, syntaxHighlighting, syntaxTree } from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import { EditorSelection, EditorState } from "@codemirror/state";
import { drawSelection, EditorView, keymap, type Command } from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import { GFM } from "@lezer/markdown";
import { toADF } from "./adf";
import { defaultRender, renderCompartment, renderConfig, trackFocus, type RenderConfig } from "./config";
import { applyEdit, editMarks, origin, type Edit } from "./edits";
import { notifyHost, resolveHost } from "./host";
import { openLanguagePicker } from "./langpicker";
import { liveField } from "./live";
import { mentionSource } from "./mentions";

const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.modifier, t.operatorKeyword, t.controlKeyword], color: "var(--sl-syn-keyword)" },
  { tag: [t.string, t.special(t.string), t.regexp], color: "var(--sl-syn-string)" },
  { tag: [t.comment, t.lineComment, t.blockComment, t.docComment], color: "var(--sl-syn-comment)", fontStyle: "italic" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "var(--sl-syn-number)" },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.macroName], color: "var(--sl-syn-function)" },
  { tag: [t.typeName, t.className, t.namespace, t.tagName], color: "var(--sl-syn-type)" },
  { tag: [t.propertyName, t.attributeName], color: "var(--sl-syn-property)" },
  { tag: [t.operator, t.punctuation, t.bracket], color: "var(--sl-syn-operator)" },
  // Markdown syntax characters, when the cursor reveals them.
  { tag: [t.processingInstruction, t.meta, t.contentSeparator], color: "var(--sl-dim)" },
]);

/** Wraps the selection in a Markdown mark such as `**`, or unwraps it. */
function toggleWrap(wrap: string): Command {
  return (view) => {
    const { state } = view;
    view.dispatch(
      state.changeByRange((range) => {
        const n = wrap.length;
        const before = state.sliceDoc(range.from - n, range.from);
        const after = state.sliceDoc(range.to, range.to + n);
        if (before === wrap && after === wrap) {
          return {
            changes: [
              { from: range.from - n, to: range.from },
              { from: range.to, to: range.to + n },
            ],
            range: EditorSelection.range(range.from - n, range.to - n),
          };
        }
        return {
          changes: [
            { from: range.from, insert: wrap },
            { from: range.to, insert: wrap },
          ],
          range: EditorSelection.range(range.from + n, range.to + n),
        };
      }),
      { userEvent: "input", scrollIntoView: true },
    );
    return true;
  };
}

const slateKeymap = [
  { key: "Mod-b", run: toggleWrap("**") },
  { key: "Mod-i", run: toggleWrap("*") },
  { key: "Mod-e", run: toggleWrap("`") },
  { key: "Mod-Shift-x", run: toggleWrap("~~") },
  { key: "Alt-l", run: (view: EditorView) => openLanguagePicker(view) },
];

/** Typing the third backtick of a fence on an empty line adds the closing fence. */
const closeFence = EditorView.inputHandler.of((view, from, to, text) => {
  if (text !== "`" || from !== to) return false;
  const line = view.state.doc.lineAt(from);
  const before = line.text.slice(0, from - line.from);
  if (!/^[ \t]*``$/.test(before) || line.text.slice(from - line.from).trim()) return false;
  for (let n = syntaxTree(view.state).resolveInner(from, -1); n.parent; n = n.parent) {
    if (n.name === "FencedCode" || n.name === "CodeBlock") return false;
  }
  const indent = before.slice(0, -2);
  view.dispatch({
    changes: { from, insert: "`\n" + indent + "\n" + indent + "```" },
    selection: { anchor: from + 2 + indent.length },
    userEvent: "input.type",
    scrollIntoView: true,
  });
  return true;
});

/** Counts document changes. The host keeps the text it was last sent with its number. */
let revision = 0;

/** Documents longer than this are not sent with each change; the host asks for them. */
const sendLimit = 256 * 1024;

const view = new EditorView({
  parent: document.getElementById("root")!,
  state: EditorState.create({
    doc: "",
    extensions: [
      history(),
      drawSelection({ cursorBlinkRate: 0 }),
      EditorView.lineWrapping,
      EditorState.tabSize.of(4),
      keymap.of([...slateKeymap, ...completionKeymap]),
      markdown({ base: commonmarkLanguage, extensions: [GFM], codeLanguages: languages, completeHTMLTags: false }),
      syntaxHighlighting(highlight),
      renderCompartment.of(renderConfig.of(defaultRender)),
      trackFocus,
      liveField,
      closeFence,
      autocompletion({ override: [mentionSource], icons: false }),
      keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
      editMarks,
      EditorView.updateListener.of((update) => {
        if (!update.docChanged) return;
        revision++;
        const by = update.transactions.map((tr) => tr.annotation(origin)).find((o) => o) ?? "user";
        const doc = update.state.doc;
        notifyHost("change", {
          rev: revision,
          origin: by,
          head: update.state.selection.main.head,
          ...(doc.length <= sendLimit ? { doc: doc.toString() } : {}),
        });
      }),
    ],
  }),
});

interface Settings {
  /** Style per construct. Missing keys keep the default. */
  render?: Partial<RenderConfig>;
  /** CSS custom properties without the `--sl-` prefix, for example `{ bg: "#000" }`. */
  theme?: Record<string, string>;
}

const api = {
  getMarkdown: (): string => view.state.doc.toString(),

  /** Replaces the document and puts the cursor at `head`. */
  setMarkdown(markdown: string, head = 0): void {
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: markdown },
      selection: { anchor: Math.min(head, markdown.length) },
      annotations: origin.of("host"),
      scrollIntoView: true,
    });
  },

  /** Replaces one exact piece of text and returns the new revision. */
  edit(edit: Edit, by: string): number {
    applyEdit(view, edit, by || "host");
    return revision;
  },

  /** The document with its revision and the cursor's place in it. */
  read: () => ({ doc: view.state.doc.toString(), rev: revision, head: view.state.selection.main.head }),

  getADF: () => toADF(view.state.doc.toString()),

  /** The selected text. With cut set, it is also removed. */
  selection(cut = false): string {
    const { state } = view;
    const text = state.selection.ranges.map((r) => state.sliceDoc(r.from, r.to)).join("\n");
    if (cut && text) view.dispatch(state.replaceSelection(""), { userEvent: "delete.cut", scrollIntoView: true });
    return text;
  },

  paste(text: string): void {
    view.dispatch(view.state.replaceSelection(text), { userEvent: "input.paste", scrollIntoView: true });
  },

  focus(on: boolean): void {
    if (on) view.focus();
    else view.contentDOM.blur();
  },

  configure(settings: Settings): void {
    if (settings.render) {
      const render = { ...defaultRender, ...settings.render };
      view.dispatch({ effects: renderCompartment.reconfigure(renderConfig.of(render)) });
    }
    for (const [name, value] of Object.entries(settings.theme ?? {})) {
      document.documentElement.style.setProperty("--sl-" + name, value);
    }
    view.requestMeasure();
  },

  openLanguagePicker: (): boolean => openLanguagePicker(view),

  resolve: resolveHost,
};

(window as unknown as { slate: typeof api }).slate = api;

document.fonts.ready.then(() => notifyHost("ready"));
