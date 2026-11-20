import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// The page's static markup and styles, read as text with comments taken out,
// so that a comment quoting a rule or a tag cannot satisfy a check.
const page = readFileSync(new URL("../index.html", import.meta.url), "utf8").replace(/<!--[\s\S]*?-->/g, "");
const styles = readFileSync(new URL("../styles.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// The declarations of every top-level rule whose selector list holds this
// exact selector, and separately those inside the phone-width media block.
function declarationsFor(selector, source = styles) {
  const found = [];
  for (const match of source.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = match[1].split(",").map((part) => part.trim().replace(/\s+/g, " "));
    if (selectors.includes(selector)) found.push(match[2]);
  }
  return found;
}

// Each @media block as [query, body], and the styles with every block taken out.
function mediaBlocks() {
  const blocks = [];
  let rest = "";
  let from = 0;
  for (let start = styles.indexOf("@media", from); start !== -1; start = styles.indexOf("@media", from)) {
    const open = styles.indexOf("{", start);
    let depth = 0;
    let at = open;
    for (; at < styles.length; at++) {
      if (styles[at] === "{") depth++;
      if (styles[at] === "}" && --depth === 0) break;
    }
    rest += styles.slice(from, start);
    blocks.push([styles.slice(start + 6, open).trim(), styles.slice(open + 1, at)]);
    from = at + 1;
  }
  return { blocks, topLevel: rest + styles.slice(from) };
}

function mediaBlock(query) {
  const found = mediaBlocks().blocks.find(([q]) => q === query);
  assert.ok(found, `no ${query} block`);
  return found[1];
}

test("a card focused under the sticky header stops far enough below it for its focus ring to show", () => {
  const margins = declarationsFor(".card-open", mediaBlocks().topLevel)
    .flatMap((body) => [...body.matchAll(/scroll-margin-top:\s*([^;]+)/g)].map((m) => m[1].trim()));
  assert.deepStrictEqual(margins, ["8px"], "the ring is 3px wide with a 3px offset");
});

test("where the header scrolls away, a focused card gets no extra margin either", () => {
  const narrow = mediaBlock("(max-width: 768px)");
  const margins = declarationsFor(".card-open", narrow)
    .flatMap((body) => [...body.matchAll(/scroll-margin-top:\s*([^;]+)/g)].map((m) => m[1].trim()));
  assert.deepStrictEqual(margins, ["0"]);
});
