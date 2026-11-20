import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// The page's static markup and styles, read as text with the comments taken
// out first, so that a comment quoting a tag or a rule cannot satisfy a check.
const page = readFileSync(new URL("../index.html", import.meta.url), "utf8").replace(/<!--[\s\S]*?-->/g, "");
const styles = readFileSync(new URL("../styles.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

function tagsWithID(id) {
  return [...page.matchAll(new RegExp(`<[a-z0-9]+\\b[^>]*\\bid="${id}"[^>]*>`, "g"))].map((match) => match[0]);
}

function attr(tag, name) {
  return tag.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1] ?? null;
}

// The declarations of every rule whose selector list holds this exact selector.
function declarationsFor(selector) {
  const found = [];
  for (const match of styles.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = match[1].split(",").map((part) => part.trim().replace(/\s+/g, " "));
    if (selectors.includes(selector)) found.push(match[2]);
  }
  return found;
}

test("each element the page's script and styles name exists once, as a tag", () => {
  for (const id of ["active-filters", "results-end", "load-more", "results-grid", "empty-state", "empty-message"]) {
    assert.equal(tagsWithID(id).length, 1, id);
  }
});

test("the filter chips' group keeps its role and its label together on the one element", () => {
  const [tag] = tagsWithID("active-filters");
  assert.equal(attr(tag, "role"), "group");
  assert.equal(attr(tag, "aria-label"), "Active filters");
  assert.equal(attr(tag, "aria-labelledby"), null, "two ways to name it would disagree");
});

test("the end-of-results line is a polite status paragraph after Load more, with nothing in it to start with", () => {
  const [tag] = tagsWithID("results-end");
  assert.ok(tag.startsWith("<p "), tag);
  assert.equal(attr(tag, "aria-live"), "polite");
  assert.equal(attr(tag, "hidden"), null, "a hidden region is not announced when it fills");
  assert.equal(attr(tag, "aria-hidden"), null);
  assert.equal(attr(tag, "role"), null);
  assert.match(page, /<p id="results-end"[^>]*><\/p>/, "empty in the markup, so the first fill is a change");
  assert.ok(page.indexOf('id="load-more"') < page.indexOf('id="results-end"'), "the line sits under the button");
  assert.ok(page.indexOf('id="results-end"') < page.indexOf("</main>"), "and inside the main region");
});

test("the end-of-results line is hidden on the stats route, as the grid and Load more are", () => {
  const rules = declarationsFor("body.route-stats #results-end");
  assert.ok(rules.length >= 1, "no rule hides it");
  assert.ok(rules.some((body) => /display:\s*none/.test(body)), rules.join("|"));
  assert.ok(declarationsFor("body.route-stats #load-more").some((body) => /display:\s*none/.test(body)));
});

test("an empty end-of-results line takes no room, and a filled one has some", () => {
  assert.ok(declarationsFor("#results-end:empty").some((body) => /margin:\s*0\b/.test(body)));
  assert.ok(declarationsFor("#results-end").some((body) => /margin:\s*[^;]*rem/.test(body)));
});

test("the header height is one number, used for the header and for focus scrolling, and it is reset where the header scrolls away", () => {
  const root = declarationsFor(":root").join(";");
  const height = root.match(/--header-height:\s*(\d+)px/);
  assert.ok(height, "no custom property");
  assert.ok(Number(height[1]) >= 104, `${height[1]}px is shorter than the header's measured 104px`);
  assert.equal([...styles.matchAll(/--header-height\s*:/g)].length, 1, "defined once");
  assert.ok(declarationsFor("html").some((body) => /scroll-padding-top:\s*var\(--header-height\)/.test(body)));
  assert.ok(declarationsFor(".site-header").some((body) => /min-height:\s*var\(--header-height\)/.test(body)));
  assert.ok(declarationsFor(".site-header").some((body) => /position:\s*sticky/.test(body)), "the padding is only right while the header is sticky");
  // At 768px and below the header is relative, so the padding must be gone.
  const narrow = styles.slice(styles.indexOf("@media (max-width: 768px)"));
  assert.match(narrow.slice(0, narrow.indexOf(".site-header")), /html\s*\{\s*scroll-padding-top:\s*0\s*;?\s*\}/);
  assert.match(narrow.slice(narrow.indexOf(".site-header")), /position:\s*relative/);
});

test("no other rule sets a scroll padding that would undo the header's", () => {
  const all = [...styles.matchAll(/scroll-padding(-top)?\s*:\s*([^;}]*)/g)].map((match) => match[2].trim());
  assert.deepStrictEqual(all, ["var(--header-height)", "0"]);
});

test("the home link's name starts with its visible words and no other control borrows the label", () => {
  const [tag] = [...page.matchAll(/<a class="brand"[^>]*>/g)].map((match) => match[0]);
  const label = attr(tag, "aria-label");
  assert.ok(label.startsWith("PokéSearch TCG archive"), label);
  assert.match(page, /<strong>PokéSearch<\/strong>\s*<small>TCG archive<\/small>/, "the label still says what is shown");
});
