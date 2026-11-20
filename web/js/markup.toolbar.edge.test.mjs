import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// The one-row toolbar of the narrowest phones has to win the cascade: the
// rules that wrap the toolbar and give the sort select a minimum width come
// earlier in the sheet, and a rule that comes after them for the same
// selector would put the second row back.
const styles = readFileSync(new URL("../styles.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// rules lists every rule of the sheet in source order, with the @media query
// it sits in ("" for none).
function rules() {
  const found = [];
  let at = 0;
  while (at < styles.length) {
    const open = styles.indexOf("{", at);
    if (open < 0) break;
    const head = styles.slice(at, open).trim();
    if (head.startsWith("@media") || head.startsWith("@supports")) {
      let depth = 1;
      let end = open + 1;
      while (end < styles.length && depth > 0) {
        if (styles[end] === "{") depth++;
        if (styles[end] === "}") depth--;
        end++;
      }
      const inner = styles.slice(open + 1, end - 1);
      for (const match of inner.matchAll(/([^{}]+)\{([^{}]*)\}/g)) found.push({ query: head, selectors: match[1].split(",").map((s) => s.trim().replace(/\s+/g, " ")), body: match[2] });
      at = end;
    } else {
      const close = styles.indexOf("}", open);
      found.push({ query: "", selectors: head.split(",").map((s) => s.trim().replace(/\s+/g, " ")), body: styles.slice(open + 1, close) });
      at = close + 1;
    }
  }
  return found;
}

const NARROW = "@media (max-width: 389.98px)";

// lastSetting is the last rule in the sheet that sets a property for a selector
// among those that can apply at 320px wide.
function lastSetting(selector, property) {
  const applies = (query) => {
    if (query === "") return true;
    const max = query.match(/^@media \(max-width:\s*([\d.]+)px\)$/);
    return Boolean(max) && Number(max[1]) >= 320;
  };
  let last = null;
  for (const rule of rules()) {
    if (!applies(rule.query) || !rule.selectors.includes(selector)) continue;
    const declared = rule.body.match(new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`));
    if (declared) last = { query: rule.query, value: declared[1].trim() };
  }
  return last;
}

test("at 320px nothing later in the sheet turns the toolbar's wrapping back on", () => {
  const last = lastSetting(".results-toolbar", "flex-wrap");
  assert.deepStrictEqual(last, { query: NARROW, value: "nowrap" });
});

test("at 320px the sort select's last minimum width is the narrow one", () => {
  assert.deepStrictEqual(lastSetting(".sort-controls select", "min-width"), { query: NARROW, value: "0" });
});

test("the narrow rules touch only the toolbar, so no width from 390px up changes", () => {
  const narrow = rules().filter((rule) => rule.query === NARROW);
  assert.ok(narrow.length > 0);
  for (const rule of narrow) {
    for (const selector of rule.selectors) {
      assert.match(selector, /^\.(results-toolbar|sort-controls)\b/, `${selector} is outside the toolbar`);
    }
  }
  const upwards = rules().filter((rule) => /min-width:\s*(3[89]\d|[4-9]\d\d)/.test(rule.query) && rule.selectors.some((s) => /results-toolbar|sort-controls/.test(s)));
  assert.deepStrictEqual(upwards, [], "a min-width query for the toolbar would leave a gap at the edge");
});

test("the narrow query ends below 390px, so 389.5px zoomed layouts are covered and 390px is not", () => {
  const narrow = rules().find((rule) => rule.query.startsWith("@media (max-width: 389"));
  assert.ok(narrow, "the narrow query exists");
  const limit = Number(narrow.query.match(/([\d.]+)px/)[1]);
  assert.ok(limit < 390 && limit > 389, `limit ${limit}`);
});
