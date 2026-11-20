import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// The compact strip: the screen width the script and the styles both call
// narrow, and the rules that keep a long figure from running under the button.
const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const styles = read("../styles.css").replace(/\/\*[\s\S]*?\*\//g, "");
const main = read("./main.js").replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");

function declarations(selector, source = styles) {
  const found = [];
  for (const match of source.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = match[1].split(",").map((part) => part.trim().replace(/\s+/g, " "));
    if (selectors.includes(selector)) found.push(match[2]);
  }
  return found.join(";");
}

test("main.js asks the one narrow query the styles use, and passes the answer on and every change after it", () => {
  const query = main.match(/matchMedia\("(\(max-width: \d+px\))"\)[^;]*;[^]*?setStripCompact/)?.[1];
  assert.ok(query, "no matchMedia call feeds setStripCompact");
  const narrow = main.match(/const (\w+) = window\.matchMedia\("\(max-width: (\d+)px\)"\);\s*\n\s*setStripCompact\(\1\.matches\)/);
  assert.ok(narrow, "the first answer is passed on");
  assert.match(main, new RegExp(`${narrow[1]}\\.addEventListener\\("change", \\(event\\) => setStripCompact\\(event\\.matches\\)\\)`));
  assert.ok(styles.includes(`@media (max-width: ${narrow[2]}px)`), "the styles use the same width");
});

test("a figure that would not fit is cut with an ellipsis inside its own box, never run under the button", () => {
  for (const selector of [".strip-value", ".strip-item"]) {
    assert.match(declarations(selector), /min-width:\s*0/, selector);
  }
  const value = declarations(".strip-value");
  assert.match(value, /overflow:\s*hidden/);
  assert.match(value, /text-overflow:\s*ellipsis/);
  assert.match(value, /white-space:\s*nowrap/);
});

test("the narrow block tightens the strip without showing or hiding it", () => {
  const start = styles.indexOf("@media (max-width: 430px)");
  assert.ok(start !== -1, "no narrow block");
  const open = styles.indexOf("{", start);
  let depth = 0;
  let at = open;
  for (; at < styles.length; at++) {
    if (styles[at] === "{") depth++;
    if (styles[at] === "}" && --depth === 0) break;
  }
  const block = styles.slice(open + 1, at);
  assert.match(block, /\.search-strip\s*\{[^}]*gap:/);
  assert.doesNotMatch(block, /\.search-strip[^{]*\{[^}]*display:/);
  assert.doesNotMatch(block, /\.rail-back[^{]*\{[^}]*display:/);
});
