import fs from "node:fs/promises";
import { getDocument } from "./node-env/node_modules/pdfjs-dist/legacy/build/pdf.mjs";

const path = process.argv[2];
if (!path) {
  throw new Error("usage: node pdfjs-probe.mjs INPUT.pdf");
}

function findInputValue(node) {
  if (node?.name === "input" || node?.name === "textarea") {
    return node.attributes?.value ?? node.value ?? null;
  }
  for (const child of node?.children ?? []) {
    const value = findInputValue(child);
    if (value !== null) {
      return value;
    }
  }
  return null;
}

const data = new Uint8Array(await fs.readFile(path));
const task = getDocument({
  data,
  enableXfa: true,
  isEvalSupported: false,
  disableAutoFetch: true,
  disableStream: true,
  disableRange: true,
  standardFontDataUrl: new URL(
    "./node-env/node_modules/pdfjs-dist/standard_fonts/",
    import.meta.url,
  ).href,
});
const document = await task.promise;
const page = await document.getPage(1);
const xfa = await page.getXfa();
const result = {
  input: path,
  isPureXfa: document.isPureXfa,
  numPages: document.numPages,
  visibleValue: findInputValue(xfa),
};
console.log(JSON.stringify(result, null, 2));
await task.destroy();
