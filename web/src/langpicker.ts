import { syntaxTree } from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import type { EditorView } from "@codemirror/view";
import type { SyntaxNode } from "@lezer/common";

const plain = "plain text";

// Names Jira knows under a different spelling.
const jiraNames: Record<string, string> = { "c#": "csharp", "f#": "fsharp", "objective-c++": "objective-c" };

interface Choice {
  name: string;
  search: string;
}

const choices: Choice[] = [
  { name: plain, search: "plain text none" },
  ...languages
    .map((l) => {
      const lower = l.name.toLowerCase();
      return { name: jiraNames[lower] ?? lower, search: [lower, ...l.alias].join(" ") };
    })
    .sort((a, b) => a.name.localeCompare(b.name)),
];

const maxShown = 8;

function fencedCodeAt(view: EditorView, pos: number): SyntaxNode | null {
  for (const side of [1, -1] as const) {
    for (let n: SyntaxNode | null = syntaxTree(view.state).resolveInner(pos, side); n; n = n.parent) {
      if (n.name === "FencedCode") return n;
    }
  }
  return null;
}

let closeOpenPicker: (() => void) | null = null;

/**
 * Opens a filterable language list for the code block at pos, or at the
 * cursor. It returns false when there is no code block there.
 */
export function openLanguagePicker(view: EditorView, pos = view.state.selection.main.head): boolean {
  const block = fencedCodeAt(view, pos);
  if (!block) return false;
  closeOpenPicker?.();
  const blockStart = block.from;

  const box = document.createElement("div");
  box.className = "sl-picker";
  const input = document.createElement("input");
  input.className = "sl-picker-input";
  input.placeholder = "Language";
  input.spellcheck = false;
  const list = document.createElement("div");
  list.className = "sl-picker-list";
  box.append(input, list);

  let shown: Choice[] = [];
  let selected = 0;

  const draw = () => {
    const query = input.value.trim().toLowerCase();
    const starts = choices.filter((c) => c.search.split(" ").some((word) => word.startsWith(query)));
    const contains = choices.filter((c) => !starts.includes(c) && c.search.includes(query));
    shown = [...starts, ...contains].slice(0, maxShown);
    selected = Math.min(selected, Math.max(0, shown.length - 1));
    list.replaceChildren(
      ...shown.map((c, i) => {
        const item = document.createElement("div");
        item.className = "sl-picker-item" + (i === selected ? " sl-picker-selected" : "");
        item.textContent = c.name;
        item.addEventListener("mousedown", (e) => {
          e.preventDefault();
          choose(c);
        });
        return item;
      }),
    );
  };

  const close = () => {
    if (closeOpenPicker !== close) return;
    closeOpenPicker = null;
    box.remove();
    view.focus();
  };

  const choose = (choice: Choice) => {
    const node = fencedCodeAt(view, blockStart);
    if (node) {
      const value = choice.name === plain ? "" : choice.name;
      const info = node.getChild("CodeInfo");
      const mark = node.getChild("CodeMark");
      if (info) view.dispatch({ changes: { from: info.from, to: info.to, insert: value } });
      else if (mark) view.dispatch({ changes: { from: mark.to, insert: value } });
    }
    close();
  };

  input.addEventListener("input", () => {
    selected = 0;
    draw();
  });
  input.addEventListener("keydown", (e) => {
    e.stopPropagation();
    if (e.key === "ArrowDown") selected = Math.min(selected + 1, shown.length - 1);
    else if (e.key === "ArrowUp") selected = Math.max(selected - 1, 0);
    else if (e.key === "Enter") {
      e.preventDefault();
      if (shown[selected]) choose(shown[selected]);
      return;
    } else if (e.key === "Escape") {
      e.preventDefault();
      close();
      return;
    } else return;
    e.preventDefault();
    draw();
  });
  input.addEventListener("blur", () => setTimeout(close, 0));

  // Place the list under the block's first line, at the right-hand side.
  const editor = view.dom.getBoundingClientRect();
  const line = view.coordsAtPos(view.state.doc.lineAt(blockStart).from);
  box.style.top = Math.max(4, (line ? line.bottom : editor.top) - editor.top + 2) + "px";
  view.dom.append(box);
  closeOpenPicker = close;
  draw();
  input.focus();
  return true;
}
