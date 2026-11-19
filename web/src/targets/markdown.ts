// Markdown as written: GitHub-flavoured, exported unchanged.

import { GFM } from "@lezer/markdown";
import type { Target } from "./index";

export const markdown: Target = {
  name: "markdown",
  syntax: [GFM],
  export: (source) => ({ mime: "text/markdown", body: source.text }),
  import: (body) => body,
};
