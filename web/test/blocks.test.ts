import { describe, expect, test } from "vitest";
import { commonmarkLanguage, markdown } from "@codemirror/lang-markdown";
import { EditorSelection, EditorState, type StateCommand, type TransactionSpec } from "@codemirror/state";
import {
  blocks,
  codeBackspace,
  codeDelete,
  detectLanguage,
  edgeLine,
  listEnter,
  listShiftEnter,
  nameLanguage,
  selectBlock,
} from "../src/blocks";
import { defaultRender, renderConfig } from "../src/config";

/** A state from text with "|" marking the cursor, or "«" and "»" a selection. */
function make(text: string): EditorState {
  const anchor = text.includes("«") ? text.indexOf("«") : text.indexOf("|");
  const head = text.includes("»") ? text.indexOf("»") - 1 : anchor;
  return EditorState.create({
    doc: text.replace(/[|«»]/g, ""),
    selection: EditorSelection.single(anchor, head),
    extensions: [
      markdown({ base: commonmarkLanguage, addKeymap: false }),
      renderConfig.of(defaultRender),
      blocks,
    ],
  });
}

/** The text with "|" at the cursor. */
function show(state: EditorState): string {
  const { from, to } = state.selection.main;
  const doc = state.doc.toString();
  if (from === to) return doc.slice(0, from) + "|" + doc.slice(from);
  return doc.slice(0, from) + "«" + doc.slice(from, to) + "»" + doc.slice(to);
}

/** Runs a command and returns the result, or null when it did not run. */
function run(cmd: StateCommand, state: EditorState): string | null {
  let out: EditorState | null = null;
  const ran = cmd({ state, dispatch: (tr) => (out = tr.state) });
  if (!ran) return null;
  return out ? show(out) : show(state);
}

const apply = (state: EditorState, spec: TransactionSpec) => show(state.update(spec).state);

describe("Enter in lists", () => {
  test("Enter after an item starts the next one", () => {
    expect(run(listEnter, make("- a|"))).toBe("- a\n- |");
  });

  test("Enter on an empty item ends the list on that line", () => {
    expect(run(listEnter, make("- a\n- |"))).toBe("- a\n|");
    expect(run(listEnter, make("- a\n- b\n- |"))).toBe("- a\n- b\n|");
  });

  test("Enter on the line that ended the list stays outside it", () => {
    expect(run(listEnter, make("- a\n|"))).toBe(null);
  });

  test("Shift+Enter adds a line inside the item, then Enter starts the next item", () => {
    const shifted = run(listShiftEnter, make("- a|"));
    expect(shifted).toBe("- a\n  |");
    expect(run(listEnter, make(shifted!))).toBe("- a\n\n- |");
  });

  test("Enter after an item in a loose list adds no blank line", () => {
    expect(run(listEnter, make("- a\n  \n- b|"))).toBe("- a\n  \n- b\n- |");
  });

  test("ordered lists count on", () => {
    expect(run(listEnter, make("1. a|"))).toBe("1. a\n2. |");
    expect(run(listEnter, make("1. a\n   |"))).toBe("1. a\n\n2. |");
  });

  test("a nested empty item moves out one level", () => {
    expect(run(listEnter, make("- a\n  - b\n  - |"))).toBe("- a\n  - b\n- |");
  });

  test("tasks continue as tasks", () => {
    expect(run(listEnter, make("- [ ] a|"))).toBe("- [ ] a\n- [ ] |");
    expect(run(listEnter, make("- [x] a\n  |"))).toBe("- [x] a\n\n- [ ] |");
  });

  test("Shift+Enter outside a list does nothing special", () => {
    expect(run(listShiftEnter, make("text|"))).toBe(null);
  });
});

describe("code block fences", () => {
  const block = "before\n```\nline 1\nline 2\n```\nafter";

  test("a click on the opening fence puts the cursor at the start of the block", () => {
    const s = make(block.replace("after", "after|"));
    expect(apply(s, { selection: { anchor: 8 }, userEvent: "select.pointer" })).toBe(
      "before\n```\n|line 1\nline 2\n```\nafter",
    );
  });

  test("a click on the closing fence puts the cursor at the end of the block", () => {
    const s = make("|" + block);
    expect(apply(s, { selection: { anchor: 26 }, userEvent: "select.pointer" })).toBe(
      "before\n```\nline 1\nline 2|\n```\nafter",
    );
  });

  test("moving on from the last line goes below the block", () => {
    const s = make("before\n```\nline 1\nline 2|\n```\nafter");
    expect(apply(s, { selection: { anchor: 26 } })).toBe("before\n```\nline 1\nline 2\n```\n|after");
  });

  test("moving back from the first line goes above the block", () => {
    const s = make("before\n```\n|line 1\nline 2\n```\nafter");
    expect(apply(s, { selection: { anchor: 9 } })).toBe("before|\n```\nline 1\nline 2\n```\nafter");
  });

  test("moving back from the line below goes into the block", () => {
    const s = make("before\n```\nline 1\nline 2\n```\n|after");
    expect(apply(s, { selection: { anchor: 26 } })).toBe("before\n```\nline 1\nline 2|\n```\nafter");
  });

  test("Down from a block that ends the document adds a line below it", () => {
    const s = make("```\ncode|\n```");
    expect(apply(s, edgeLine(s, true, 3)!)).toBe("```\ncode\n```\n|");
    expect(edgeLine(make("```\ncode|\n```\nafter"), true, 3)).toBe(null);
  });

  test("Up from a block that starts the document adds a line above it", () => {
    const s = make("```\n|code\n```");
    expect(apply(s, edgeLine(s, false, 1)!)).toBe("|\n```\ncode\n```");
  });

  test("Backspace in an empty block deletes it", () => {
    expect(run(codeBackspace, make("a\n```\n|\n```\nb"))).toBe("a\n|\nb");
  });

  test("Backspace at the start of a block with text does nothing", () => {
    expect(run(codeBackspace, make("a\n```\n|x\n```\nb"))).toBe("a\n```\n|x\n```\nb");
  });

  test("Backspace below a block steps into it", () => {
    expect(run(codeBackspace, make("```\nx\n```\n|b"))).toBe("```\nx|\n```\nb");
    expect(run(codeBackspace, make("```\nx\n```\n|"))).toBe("```\nx|\n```");
  });

  test("Delete at the end of a block does nothing, and above one steps into it", () => {
    expect(run(codeDelete, make("```\nx|\n```\nb"))).toBe("```\nx|\n```\nb");
    expect(run(codeDelete, make("a|\n```\nx\n```"))).toBe("a\n```\n|x\n```");
    expect(run(codeDelete, make("a\n|\n```\nx\n```"))).toBe("a\n```\n|x\n```");
  });

  test("a selection reaching out of the block keeps its ends", () => {
    const s = make("|a\n```\nx\n```");
    expect(apply(s, { selection: { anchor: 0, head: s.doc.length } })).toBe("«a\n```\nx\n```»");
  });

  test("the fences are left alone in live style", () => {
    const s = EditorState.create({
      doc: "```\nx\n```\nb",
      extensions: [markdown({ addKeymap: false }), renderConfig.of({ ...defaultRender, code: "live" }), blocks],
    });
    expect(s.update({ selection: { anchor: 1 }, userEvent: "select.pointer" }).state.selection.main.head).toBe(1);
  });
});

describe("Mod-a", () => {
  test("in a block it selects the block's text, then everything", () => {
    const s = make("a\n```\nx|\ny\n```\nb");
    const once = run(selectBlock, s);
    expect(once).toBe("a\n```\n«x\ny»\n```\nb");
    expect(run(selectBlock, make(once!))).toBe(null);
  });

  test("outside a block it falls through", () => {
    expect(run(selectBlock, make("a|\n```\nx\n```"))).toBe(null);
  });
});

describe("language detection", () => {
  test("names common languages", () => {
    expect(detectLanguage("def greet(name):\n    print(f'hello {name}')\n    return None\n")).toBe("python");
    expect(detectLanguage('package main\n\nimport "fmt"\n\nfunc main() {\n\tfmt.Println("hi")\n}\n')).toBe("go");
    expect(detectLanguage('{\n  "name": "x",\n  "items": [1, 2, 3],\n  "ok": true\n}\n')).toBe("json");
    expect(detectLanguage("import os\n\ndef main():\n    for name in os.listdir('.'):\n        print(name)\n")).toBe(
      "python",
    );
    expect(detectLanguage("SELECT id, name FROM users WHERE id > 10 ORDER BY id;")).toBe("sql");
    expect(detectLanguage("public static void main(String[] args) {\n    System.out.println(\"hi\");\n}")).toBe("java");
    expect(
      detectLanguage("public class Foo {\n    public int Bar { get; set; }\n    public void Baz() { Console.WriteLine(Bar); }\n}"),
    ).toBe("csharp");
  });

  test("gives no name rather than PHP for shell variables", () => {
    expect(detectLanguage('for f in *.log; do\n  grep -c ERROR "$f"\ndone')).not.toBe("php");
  });

  test("says nothing about plain words", () => {
    expect(detectLanguage("hello")).toBe("");
  });

  test("names a pasted block that has no language, and only that", () => {
    const code = "def greet(name):\n    print(f'hello {name}')\n    return None";
    const s = make("```\n" + code + "|\n```");
    expect(apply(s, nameLanguage(s, s.selection.main.head)!).startsWith("```python\n")).toBe(true);
    const named = make("```sql\n" + code + "|\n```");
    expect(nameLanguage(named, named.selection.main.head)).toBe(null);
  });
});
