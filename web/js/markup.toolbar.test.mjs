import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// The toolbar above the grid on the narrowest phones: whatever the count line
// says (the markup's loading text, a count, a dash), the sort control stays on
// its row, so the toolbar keeps one height before and after the first answer.
const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const styles = read("../styles.css").replace(/\/\*[\s\S]*?\*\//g, "");

// mediaBlocks lists every @media block with the body between its braces.
function mediaBlocks() {
  const blocks = [];
  for (const match of styles.matchAll(/@media([^{]*)\{/g)) {
    const open = match.index + match[0].length - 1;
    let depth = 0;
    let at = open;
    for (; at < styles.length; at++) {
      if (styles[at] === "{") depth++;
      if (styles[at] === "}" && --depth === 0) break;
    }
    blocks.push({ query: match[1].trim(), body: styles.slice(open + 1, at) });
  }
  return blocks;
}

function declarations(selector, source) {
  const found = [];
  for (const match of source.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = match[1].split(",").map((part) => part.trim().replace(/\s+/g, " "));
    if (selectors.includes(selector)) found.push(match[2]);
  }
  return found.join(";");
}

// The blocks that apply below 390px wide and at no width from 390 up.
function belowPhoneBlocks() {
  return mediaBlocks().filter(({ query }) => {
    const max = query.match(/^\(max-width:\s*([\d.]+)px\)$/);
    return max && Number(max[1]) < 390 && Number(max[1]) >= 320;
  });
}

test("below 390px the toolbar never wraps, so the count line cannot push the sort control to a second row", () => {
  const blocks = belowPhoneBlocks();
  assert.ok(blocks.length > 0, "no block for widths from 320px to just under 390px");
  const body = blocks.map((block) => block.body).join("\n");
  assert.match(declarations(".results-toolbar", body), /flex-wrap:\s*nowrap/);
});

test("below 390px the sort control gives way instead of the count, which is never cut", () => {
  const body = belowPhoneBlocks().map((block) => block.body).join("\n");
  assert.match(declarations(".sort-controls", body), /min-width:\s*0/);
  assert.match(declarations(".sort-controls label", body), /min-width:\s*0/);
  assert.match(declarations(".sort-controls select", body), /min-width:\s*0/);
  assert.match(declarations(".results-toolbar > div:first-child", body), /flex:\s*none/);
  assert.doesNotMatch(declarations("#total-count", body), /overflow|text-overflow/);
});

test("from 390px up the toolbar keeps the layout it had", () => {
  const phone = mediaBlocks().find(({ query }) => query === "(max-width: 500px)");
  assert.ok(phone, "the phone block is still there");
  assert.match(declarations(".results-toolbar", phone.body), /flex-wrap:\s*wrap/);
  for (const { query, body } of mediaBlocks()) {
    const max = query.match(/^\(max-width:\s*([\d.]+)px\)$/);
    if (max && Number(max[1]) >= 390) {
      assert.doesNotMatch(declarations(".results-toolbar", body), /nowrap/, query);
    }
  }
});
