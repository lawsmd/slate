// Bundles the editor into one HTML file, which the Go package embeds.

import { build } from "esbuild";
import { mkdir, readFile, writeFile } from "node:fs/promises";

const result = await build({
  entryPoints: ["src/main.ts"],
  bundle: true,
  minify: true,
  format: "iife",
  target: "chrome120",
  legalComments: "none",
  write: false,
});

// The script is inlined, so a closing tag inside it would end it early.
const js = result.outputFiles[0].text.replaceAll("</script", "<\\/script");
const css = await readFile("src/style.css", "utf8");
const html = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>slate</title>
<style>
${css}</style>
</head>
<body>
<div id="root"></div>
<script>${js}</script>
</body>
</html>
`;

await mkdir("dist", { recursive: true });
await writeFile("dist/index.html", html);
console.log(`dist/index.html ${(html.length / 1024).toFixed(0)} KB`);
