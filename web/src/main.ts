import { autocompletion, completionKeymap } from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { commonmarkLanguage, markdown } from "@codemirror/lang-markdown";
import { HighlightStyle, syntaxHighlighting, syntaxTree } from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import { Compartment, EditorSelection, EditorState, StateField } from "@codemirror/state";
import { Decoration, type DecorationSet, drawSelection, EditorView, keymap, type Command } from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import { toDoc } from "./doc";
import { defaultTarget, parse, target, type Exported, type Target } from "./targets";
import { defaultRender, renderCompartment, renderConfig, trackFocus, type RenderConfig } from "./config";
import { blink, setSteady } from "./blink";
import { scrollbar } from "./scrollbar";
import { showSelection } from "./selected";
import { blocks } from "./blocks";
import { applyEdit, editMarks, origin, written, writtenText, type Edit } from "./edits";
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

/** The target the document is written for. */
let current: Target = target(defaultTarget);

const language = (tg: Target) =>
  markdown({
    base: commonmarkLanguage,
    extensions: tg.syntax,
    codeLanguages: languages,
    completeHTMLTags: false,
    addKeymap: false, // blocks.ts binds Enter and Backspace
  });
const languageCompartment = new Compartment();

/** Underlines what the current target cannot carry as written. */
const lossMarks = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(marks, tr) {
    if (!tr.docChanged && !tr.reconfigured) return marks;
    const found = current.unsupported?.(parse(current, tr.newDoc.toString())) ?? [];
    return Decoration.set(
      found
        .filter((loss) => loss.to > loss.from)
        .map((loss) =>
          Decoration.mark({ class: "sl-unsupported", attributes: { title: loss.reason } }).range(loss.from, loss.to),
        ),
      true,
    );
  },
  provide: (field) => EditorView.decorations.from(field),
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
      drawSelection({ cursorBlinkRate: 0 }), // blink.ts blinks the cursor
    blink,
      showSelection,
      scrollbar,
      EditorView.lineWrapping,
      EditorState.tabSize.of(4),
      keymap.of([...slateKeymap, ...completionKeymap]),
      languageCompartment.of(language(current)),
      lossMarks,
      syntaxHighlighting(highlight),
      renderCompartment.of(renderConfig.of(defaultRender)),
      trackFocus,
      liveField,
      closeFence,
      blocks,
      autocompletion({ override: [mentionSource], icons: false }),
      keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
      editMarks,
      written,
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
  /** The name of the target to write for. */
  target?: string;
  /** Whether the cursor shows without blinking. */
  steady?: boolean;
}

/** The host's own render choices, kept so a change of target can rebuild on them. */
let hostRender: Partial<RenderConfig> = {};

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
    applyEdit(view, edit, by || "host", revision);
    return revision;
  },

  /** The document with its revision, the text the user has selected and each writer's text. */
  read() {
    const { state } = view;
    const selected = state.selection.ranges.map((r) => state.sliceDoc(r.from, r.to)).join("\n");
    return { markdown: state.doc.toString(), rev: revision, selected, written: writtenText(view) };
  },

  /** The document in a target's form: the current target's, or the named one's. */
  export(name = ""): Exported {
    const tg = name ? target(name) : current;
    return tg.export(parse(tg, view.state.doc.toString()));
  },

  /**
   * Replaces the document with one in the current target's form. Returns
   * what the document held that Markdown has no form for and was left out.
   */
  import(body: string): string[] {
    if (!current.import) throw new Error(`the ${current.name} target cannot import`);
    const markdown = current.import(body);
    api.setMarkdown(markdown);
    return current.importLoss?.(body) ?? [];
  },

  /** The syntax tree as plain data, parsed with the current target's syntax. */
  tree: () => toDoc(parse(current, view.state.doc.toString())),

  /** What the current target cannot carry as written. */
  unsupported: () => current.unsupported?.(parse(current, view.state.doc.toString())) ?? [],

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
    const effects = [];
    if (settings.target && settings.target !== current.name) {
      current = target(settings.target);
      effects.push(languageCompartment.reconfigure(language(current)));
    }
    if (settings.render) hostRender = settings.render;
    if (settings.render || effects.length) {
      // The host's choices win over the target's, which win over the defaults.
      const render = { ...defaultRender, ...current.render, ...hostRender };
      effects.push(renderCompartment.reconfigure(renderConfig.of(render)));
    }
    if (effects.length) view.dispatch({ effects });
    for (const [name, value] of Object.entries(settings.theme ?? {})) {
      document.documentElement.style.setProperty("--sl-" + name, value);
    }
    if (settings.steady !== undefined) setSteady(settings.steady);
    view.requestMeasure();
  },

  openLanguagePicker: (): boolean => openLanguagePicker(view),

  resolve: resolveHost,
};

(window as unknown as { slate: typeof api }).slate = api;

document.fonts.ready.then(() => notifyHost("ready"));
