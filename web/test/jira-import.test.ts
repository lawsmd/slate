import { describe, expect, test } from "vitest";
import { toADF } from "../src/targets/jira";
import { fromADF, lostOnImport } from "../src/targets/jira-import";

// Markdown that must come back from ADF as text that exports to the same ADF.
const samples: Record<string, string> = {
  marks: "Plain, **bold**, *italic*, ~~struck~~, `code` and a [link](https://example.com/a_b).",
  "nested marks": "**bold with *italic* inside** and [a **bold** link](https://example.com).",
  headings: "# One\n\n## Two\n\n###### Six",
  "line breaks": "first line\nsecond line",
  quote: "> quoted text\n> on two lines\n>\n> and a second paragraph",
  lists: "- one\n- two\n  - nested\n  - again\n- three",
  ordered: "3. three\n4. four",
  "list with blocks": "1. item\n\n   ```go\n   x := 1\n   ```\n2. next",
  tasks: "- [ ] open\n- [x] done\n  - [ ] under it",
  code: "```ts\nconst a = `b`;\n\n  indented\n```",
  "code holding a fence": "````\n```\ninner\n```\n````",
  rule: "above\n\n---\n\nbelow",
  table: "| a | b |\n| --- | --- |\n| 1 | **2** |\n| x \\| y |  |",
  callout: "> [!warning] Careful\n> the body\n>\n> - a list",
  "callout without a title": "> [!info]\n> only a body",
  mention: "ask [@Ada Lovelace](mention:id%3A1) first",
  "syntax characters": "2 * 3 * 4, a_b, _x_, [not a link], <b>, `tick, a | b, back\\slash",
  "lines that look like blocks": "\\# not a heading\n\\> not a quote\n\\- not a list\n1\\. not a list",
};

describe("fromADF", () => {
  for (const [name, markdown] of Object.entries(samples)) {
    test(`${name} returns as Markdown that exports the same`, () => {
      const adf = toADF(markdown);
      const back = fromADF(adf);
      expect(toADF(back), back).toEqual(adf);
    });
  }

  test("writes the Markdown a person would", () => {
    const adf = toADF("# Title\n\nSome **bold** text.\n\n- [x] done\n\n> [!note] Hi\n> there");
    expect(fromADF(JSON.stringify(adf))).toBe("# Title\n\nSome **bold** text.\n\n- [x] done\n\n> [!note] Hi\n> there");
  });

  test("keeps the text of nodes Markdown has no form for", () => {
    const doc = {
      version: 1,
      type: "doc",
      content: [
        {
          type: "paragraph",
          content: [
            { type: "text", text: "red", marks: [{ type: "textColor", attrs: { color: "#ff0000" } }] },
            { type: "text", text: " " },
            { type: "emoji", attrs: { shortName: ":smile:", text: "😄" } },
            { type: "text", text: " " },
            { type: "status", attrs: { text: "DONE", color: "green" } },
            { type: "text", text: " " },
            { type: "inlineCard", attrs: { url: "https://example.com/x" } },
            { type: "text", text: " " },
            { type: "date", attrs: { timestamp: "1700000000000" } },
          ],
        },
        { type: "expand", attrs: { title: "More" }, content: [{ type: "paragraph", content: [{ type: "text", text: "inside" }] }] },
      ],
    };
    expect(fromADF(doc)).toBe("red 😄 DONE [https://example.com/x](https://example.com/x) 2023-11-14\n\ninside");
    expect(lostOnImport(doc)).toEqual([]);
  });

  test("reports attachments it leaves out", () => {
    const doc = {
      version: 1,
      type: "doc",
      content: [
        { type: "paragraph", content: [{ type: "text", text: "see the picture" }] },
        { type: "mediaSingle", content: [{ type: "media", attrs: { id: "abc", type: "file", collection: "" } }] },
      ],
    };
    expect(fromADF(doc)).toBe("see the picture");
    expect(lostOnImport(JSON.stringify(doc))).toEqual(["media"]);
  });

  test("refuses what is not an ADF document", () => {
    expect(() => fromADF('{"type":"paragraph"}')).toThrow(/not an ADF document/);
  });
});
