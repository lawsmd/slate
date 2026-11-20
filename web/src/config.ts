import { Compartment, Facet, StateEffect, StateField } from "@codemirror/state";
import { EditorView } from "@codemirror/view";

/**
 * How one Markdown construct is drawn.
 *
 * - `live`: the Obsidian behaviour. Syntax characters hide until the cursor
 *   reaches them.
 * - `block`: the Jira behaviour. The construct is drawn as a box or widget
 *   and its syntax shows only while the cursor is on the syntax line itself.
 *   A code block's fences never show: the cursor cannot reach them, and the
 *   language is chosen from the block's header.
 * - `raw`: the Markdown text as typed.
 *
 * A construct with no renderer for `block` is drawn as `live`.
 */
export type Style = "live" | "block" | "raw";

export interface RenderConfig {
  heading: Style;
  emphasis: Style;
  link: Style;
  list: Style;
  quote: Style;
  callout: Style;
  code: Style;
  table: Style;
  rule: Style;
  mention: Style;
}

export const defaultRender: RenderConfig = {
  heading: "live",
  emphasis: "live",
  link: "live",
  list: "live",
  quote: "live",
  callout: "live",
  code: "block",
  table: "block",
  rule: "live",
  mention: "live",
};

export const renderConfig = Facet.define<RenderConfig, RenderConfig>({
  combine: (values) => values[0] ?? defaultRender,
});

export const renderCompartment = new Compartment();

const setFocused = StateEffect.define<boolean>();

/** Whether the editor has focus. Without focus no syntax is revealed. */
export const focusField = StateField.define<boolean>({
  create: () => false,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setFocused)) value = e.value;
    return value;
  },
});

export const trackFocus = [
  focusField,
  EditorView.focusChangeEffect.of((_, focusing) => setFocused.of(focusing)),
];
