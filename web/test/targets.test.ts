import { describe, expect, test } from "vitest";
import { toDoc, type DocNode } from "../src/doc";
import { parse, target, targets } from "../src/targets";

describe("targets", () => {
  test("every target exports the document it is given", () => {
    for (const t of Object.values(targets)) {
      const out = t.export(parse(t, "# Title\n\nSome *text*.\n"));
      expect(out.mime, t.name).not.toBe("");
      expect(out.body, t.name).not.toBe("");
    }
  });

  test("an unknown name is an error that lists the names", () => {
    expect(() => target("nowhere")).toThrow(/markdown, jira/);
  });

  test("markdown exports and imports the text unchanged", () => {
    const text = "- [ ] a task\n\n| a | b |\n|---|---|\n| 1 | 2 |\n";
    const t = target("markdown");
    expect(t.export(parse(t, text))).toEqual({ mime: "text/markdown", body: text });
    expect(t.import!(text)).toBe(text);
  });

  test("jira exports ADF", () => {
    const t = target("jira");
    const out = t.export(parse(t, "Hello **there**."));
    expect(out.mime).toBe("application/json");
    expect(JSON.parse(out.body)).toEqual({
      version: 1,
      type: "doc",
      content: [
        {
          type: "paragraph",
          content: [
            { type: "text", text: "Hello " },
            { type: "text", text: "there", marks: [{ type: "strong" }] },
            { type: "text", text: "." },
          ],
        },
      ],
    });
  });

  test("jira names what it cannot carry", () => {
    const t = target("jira");
    const text = "Fine text.\n\n![chart](chart.png)\n\n<div>raw</div>\n";
    const found = t.unsupported!(parse(t, text));
    expect(found.map((loss) => text.slice(loss.from, loss.to))).toEqual(["![chart](chart.png)", "<div>raw</div>"]);
    expect(found[0].reason).toMatch(/link/);
    expect(t.unsupported!(parse(t, "Plain **text** with a [link](https://example.com)."))).toEqual([]);
  });
});

describe("the tree for a host", () => {
  test("offsets count UTF-8 bytes", () => {
    const text = "é *ü* 😀 `x`";
    const doc = toDoc(parse(target("markdown"), text));
    const bytes = new TextEncoder().encode(doc.markdown);
    const slice = (n: DocNode) => new TextDecoder().decode(bytes.slice(n.from, n.to));
    expect(doc.root.type).toBe("Document");
    expect(slice(doc.root)).toBe(text);
    const paragraph = doc.root.children![0];
    expect(paragraph.type).toBe("Paragraph");
    const byType = (type: string) => paragraph.children!.find((c) => c.type === type)!;
    expect(slice(byType("Emphasis"))).toBe("*ü*");
    expect(slice(byType("InlineCode"))).toBe("`x`");
  });
});
