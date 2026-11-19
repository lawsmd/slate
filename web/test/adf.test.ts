import { describe, expect, test } from "vitest";
import Ajv from "ajv-draft-04";
import schema from "./adf-schema.json";
import { toADF, type ADFNode } from "../src/adf";

const validate = new Ajv({ strict: false, allErrors: true }).compile(schema);

/** Converts and checks the result against Atlassian's schema. */
function adf(markdown: string): ADFNode[] {
  const doc = toADF(markdown);
  if (!validate(doc)) {
    throw new Error("invalid ADF: " + JSON.stringify(validate.errors?.slice(0, 3), null, 1) + "\n" + JSON.stringify(doc));
  }
  return doc.content!;
}

const text = (s: string, ...marks: string[]) =>
  marks.length ? { type: "text", text: s, marks: marks.map((type) => ({ type })) } : { type: "text", text: s };
const para = (...content: unknown[]) => (content.length ? { type: "paragraph", content } : { type: "paragraph" });
const fence = "```";

describe("blocks", () => {
  test("empty document", () => {
    expect(toADF("")).toEqual({ version: 1, type: "doc", content: [] });
  });

  test("paragraphs and headings", () => {
    expect(adf("# Title\n\nFirst.\n\n### Third level\n\nSetext\n---")).toEqual([
      { type: "heading", attrs: { level: 1 }, content: [text("Title")] },
      para(text("First.")),
      { type: "heading", attrs: { level: 3 }, content: [text("Third level")] },
      { type: "heading", attrs: { level: 2 }, content: [text("Setext")] },
    ]);
  });

  test("a line break in a paragraph is a hard break", () => {
    expect(adf("one\ntwo  \nthree\\\nfour")).toEqual([
      para(text("one"), { type: "hardBreak" }, text("two"), { type: "hardBreak" }, text("three"), { type: "hardBreak" }, text("four")),
    ]);
  });

  test("code block with a language", () => {
    expect(adf(fence + "go\nfunc main() {\n\tprintln(1)\n}\n" + fence)).toEqual([
      { type: "codeBlock", attrs: { language: "go" }, content: [text("func main() {\n\tprintln(1)\n}")] },
    ]);
  });

  test("code block without a language, and empty", () => {
    expect(adf(`${fence}\nx\n${fence}\n\n${fence}\n${fence}`)).toEqual([
      { type: "codeBlock", content: [text("x")] },
      { type: "codeBlock" },
    ]);
  });

  test("code block inside a quote drops the quote prefix", () => {
    expect(adf(`> ${fence}js\n> a\n> b\n> ${fence}`)).toEqual([
      { type: "blockquote", content: [{ type: "codeBlock", attrs: { language: "js" }, content: [text("a\nb")] }] },
    ]);
  });

  test("rule and quote", () => {
    expect(adf("> quoted\n> again\n\n---")).toEqual([
      { type: "blockquote", content: [para(text("quoted"), { type: "hardBreak" }, text("again"))] },
      { type: "rule" },
    ]);
  });

  test("bullet and ordered lists, nested", () => {
    expect(adf("- a\n- b\n  1. one\n  2. two\n\n3. three\n4. four")).toEqual([
      {
        type: "bulletList",
        content: [
          { type: "listItem", content: [para(text("a"))] },
          {
            type: "listItem",
            content: [
              para(text("b")),
              {
                type: "orderedList",
                content: [
                  { type: "listItem", content: [para(text("one"))] },
                  { type: "listItem", content: [para(text("two"))] },
                ],
              },
            ],
          },
        ],
      },
      {
        type: "orderedList",
        attrs: { order: 3 },
        content: [
          { type: "listItem", content: [para(text("three"))] },
          { type: "listItem", content: [para(text("four"))] },
        ],
      },
    ]);
  });

  test("task list", () => {
    expect(adf("- [ ] open\n- [x] **done**")).toEqual([
      {
        type: "taskList",
        attrs: { localId: "slate-3" },
        content: [
          { type: "taskItem", attrs: { localId: "slate-1", state: "TODO" }, content: [text("open")] },
          { type: "taskItem", attrs: { localId: "slate-2", state: "DONE" }, content: [text("done", "strong")] },
        ],
      },
    ]);
  });

  test("a list mixing tasks and plain items stays a bullet list", () => {
    expect(adf("- [ ] task\n- plain")).toEqual([
      {
        type: "bulletList",
        content: [
          { type: "listItem", content: [para(text("[ ] task"))] },
          { type: "listItem", content: [para(text("plain"))] },
        ],
      },
    ]);
  });

  test("callout becomes a panel", () => {
    expect(adf("> [!warning] Careful\n> This is `risky`.\n>\n> - one")).toEqual([
      {
        type: "panel",
        attrs: { panelType: "warning" },
        content: [
          para(text("Careful", "strong")),
          para(text("This is "), text("risky", "code"), text(".")),
          { type: "bulletList", content: [{ type: "listItem", content: [para(text("one"))] }] },
        ],
      },
    ]);
    expect(adf("> [!tip]\n> body")).toEqual([
      { type: "panel", attrs: { panelType: "success" }, content: [para(text("body"))] },
    ]);
  });

  test("table with an empty cell", () => {
    const cell = (type: string, ...content: unknown[]) => ({ type, attrs: {}, content: [para(...content)] });
    expect(adf("| Name | Value |\n| --- | --- |\n| a | **1** |\n| b | |")).toEqual([
      {
        type: "table",
        attrs: { isNumberColumnEnabled: false, layout: "default" },
        content: [
          { type: "tableRow", content: [cell("tableHeader", text("Name")), cell("tableHeader", text("Value"))] },
          { type: "tableRow", content: [cell("tableCell", text("a")), cell("tableCell", text("1", "strong"))] },
          { type: "tableRow", content: [cell("tableCell", text("b")), cell("tableCell")] },
        ],
      },
    ]);
  });
});

describe("inline", () => {
  test("marks", () => {
    expect(adf("a **bold** *em* ~~gone~~ `code` ***both***")).toEqual([
      para(
        text("a "), text("bold", "strong"), text(" "), text("em", "em"), text(" "),
        text("gone", "strike"), text(" "), text("code", "code"), text(" "), text("both", "em", "strong"),
      ),
    ]);
  });

  test("links", () => {
    const link = (s: string, href: string) => ({ type: "text", text: s, marks: [{ type: "link", attrs: { href } }] });
    expect(adf("[site](https://example.com) <https://a.example> www.example.org [bare]")).toEqual([
      para(
        link("site", "https://example.com"), text(" "), link("https://a.example", "https://a.example"), text(" "),
        link("www.example.org", "http://www.example.org"), text(" [bare]"),
      ),
    ]);
  });

  test("mention", () => {
    expect(adf("cc [@Ada Lovelace](mention:5b10ac8d%3Aabc) please")).toEqual([
      para(
        text("cc "),
        { type: "mention", attrs: { id: "5b10ac8d:abc", text: "@Ada Lovelace" } },
        text(" please"),
      ),
    ]);
  });

  test("escapes and entities", () => {
    expect(adf("\\*not em\\* &amp; &#65;")).toEqual([para(text("*not em* & A"))]);
  });
});

test("a full comment validates", () => {
  adf(`# Release notes

Deployed by [@Sam](mention:abc123). See [the runbook](https://example.com/runbook).

> [!info] Heads up
> Restart **after** the migration.

- [x] migrate
- [ ] restart

1. first
   ${fence}sh
   make deploy
   ${fence}
2. second

| Service | State |
| --- | --- |
| api | ok |

---

${fence}json
{"ok": true}
${fence}
`);
});
