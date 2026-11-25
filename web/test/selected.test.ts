import { describe, expect, test } from "vitest";
import { EditorSelection, EditorState } from "@codemirror/state";
import { selectedMarks } from "../src/selected";

/** A state from text with "«" and "»" marking the selection. */
function make(text: string): EditorState {
  const anchor = text.indexOf("«");
  const head = text.indexOf("»") - 1;
  return EditorState.create({ doc: text.replace(/[«»]/g, ""), selection: EditorSelection.single(anchor, head) });
}

/** Each mark as "line N" for a whole line or the text it covers. */
function marks(state: EditorState, from?: number, to?: number): string[] {
  return selectedMarks(state, from, to).map((m) =>
    m.from === m.to ? `line ${state.doc.lineAt(m.from).number}` : state.sliceDoc(m.from, m.to),
  );
}

describe("selectedMarks", () => {
  test("a cursor marks nothing", () => {
    expect(marks(make("abc«»def"))).toEqual([]);
  });

  test("text within one line is marked and the line is not", () => {
    expect(marks(make("a«bc»d"))).toEqual(["bc"]);
  });

  test("lines selected past their end are marked whole, the last line only in part", () => {
    expect(marks(make("```\n«one\n\ntwo\nthr»ee\n```"))).toEqual([
      "line 2",
      "one",
      "line 3",
      "line 4",
      "two",
      "thr",
    ]);
  });

  test("a selection of the whole document marks every line", () => {
    const state = make("«```\nx = 1\n```»");
    expect(marks(state)).toEqual(["line 1", "```", "line 2", "x = 1", "line 3", "```"]);
  });

  test("only the lines in the range asked for are marked", () => {
    const state = make("«aa\nbb\ncc»");
    expect(marks(state, 3, 5)).toEqual(["line 2", "bb"]);
  });
});
