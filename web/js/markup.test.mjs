import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// The page's static markup and styles, read as text: the checks here are
// about attributes and declarations, not about anything a script does.
const page = readFileSync(new URL("../index.html", import.meta.url), "utf8");
const styles = readFileSync(new URL("../styles.css", import.meta.url), "utf8");

function openingTag(pattern) {
  const match = page.match(pattern);
  assert.ok(match, `no tag matches ${pattern}`);
  return match[0];
}

function rule(selector) {
  const at = styles.search(new RegExp(`(^|\\n)${selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{`));
  assert.ok(at !== -1, `no top-level rule for ${selector}`);
  return styles.slice(at, styles.indexOf("}", at));
}

test("the filter chips' container carries a role its label can name", () => {
  const tag = openingTag(/<div id="active-filters"[^>]*>/);
  assert.match(tag, /aria-label="Active filters"/);
  assert.match(tag, /role="group"/);
});

test("the home link's accessible name starts with the text it shows", () => {
  const tag = openingTag(/<a class="brand"[^>]*>/);
  const label = tag.match(/aria-label="([^"]*)"/)?.[1] ?? "";
  assert.ok(label.startsWith("PokéSearch TCG archive"), label);
});

test("focus scrolls clear of the sticky header, measured by the header's own height", () => {
  assert.match(rule(":root"), /--header-height:\s*\d+px;/);
  assert.match(rule("html"), /scroll-padding-top:\s*var\(--header-height\)/);
  assert.match(rule(".site-header"), /min-height:\s*var\(--header-height\)/);
});

test("the end-of-results line is a polite live region, not a button", () => {
  const tag = openingTag(/<p id="results-end"[^>]*>/);
  assert.match(tag, /aria-live="polite"/);
  assert.doesNotMatch(tag, /role="button"|tabindex/);
});
