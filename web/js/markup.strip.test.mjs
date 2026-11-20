import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// The strip above the grid, read as text: its markup, where it sits, the
// widths it shows at, and how main.js wires it. Comments are taken out first so
// a comment naming a tag or a rule cannot satisfy a check.
const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const page = read("../index.html").replace(/<!--[\s\S]*?-->/g, "");
const styles = read("../styles.css").replace(/\/\*[\s\S]*?\*\//g, "");
const main = read("./main.js").replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");

function tagsWithID(id) {
  return [...page.matchAll(new RegExp(`<[a-z0-9]+\\b[^>]*\\bid="${id}"[^>]*>`, "g"))].map((match) => match[0]);
}

function attr(tag, name) {
  return tag.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1] ?? null;
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

function declarationsFor(selector, source) {
  const found = [];
  for (const match of source.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = match[1].split(",").map((part) => part.trim().replace(/\s+/g, " "));
    if (selectors.includes(selector)) found.push(match[2]);
  }
  return found;
}

const IDS = [
  "search-strip", "strip-es", "strip-es-name", "strip-roundtrip", "strip-roundtrip-name",
  "strip-count", "strip-count-name", "view-query", "rail-back",
];

test("the strip's elements exist once each", () => {
  for (const id of IDS) assert.equal(tagsWithID(id).length, 1, id);
});

test("the strip is a labelled group inside the main column, between the toolbar and the grid", () => {
  const [tag] = tagsWithID("search-strip");
  assert.equal(attr(tag, "role"), "group");
  assert.ok((attr(tag, "aria-label") ?? "").length > 0, "a group with no name");
  assert.equal(attr(tag, "aria-live"), null, "its numbers must not interrupt");
  assert.equal(attr(tag, "hidden"), null, "it is hidden by width, not by the attribute");
  const at = page.indexOf('id="search-strip"');
  assert.ok(page.indexOf('class="results-toolbar"') < at, "after the toolbar");
  assert.ok(at < page.indexOf('id="results-grid"'), "before the grid");
  assert.ok(at > page.indexOf("<main") && at < page.indexOf("</main>"), "inside main");
  const strip = page.slice(at, page.indexOf("</div>", page.indexOf('id="view-query"')));
  assert.doesNotMatch(strip, /aria-live|role="status"|role="alert"/);
});

test("the strip starts with the same placeholders the script writes, so it keeps its height", () => {
  for (const id of ["strip-es", "strip-roundtrip", "strip-count"]) {
    const content = page.match(new RegExp(`id="${id}"[^>]*>([^<]*)<`))?.[1];
    assert.equal(content, "—", id);
  }
  assert.doesNotMatch(page.slice(page.indexOf('id="search-strip"'), page.indexOf('id="results-grid"')), /\d+ ?ms|\d+ cards/);
});

test("the numbers are read out in full and shown short, and only one of the two is announced", () => {
  for (const id of ["strip-es", "strip-roundtrip", "strip-count"]) {
    assert.equal(attr(tagsWithID(id)[0], "aria-hidden"), "true", `${id} is the short form`);
    assert.match(attr(tagsWithID(`${id}-name`)[0], "class") ?? "", /\bsr-only\b/, `${id}-name is the full form`);
  }
});

test("View query is a real button that names where it goes, and the rail has a way back", () => {
  const [tag] = tagsWithID("view-query");
  assert.ok(tag.startsWith("<button "), tag);
  assert.equal(attr(tag, "type"), "button");
  assert.ok((attr(tag, "aria-label") ?? "").startsWith("View query"), "the name starts with the words shown");
  assert.match(page, /<button id="view-query"[^>]*>View query<\/button>/);
  const [back] = tagsWithID("rail-back");
  assert.ok(back.startsWith("<button "), back);
  assert.equal(attr(back, "type"), "button");
  assert.match(page, /<button id="rail-back"[^>]*>[^<]*Back to results<\/button>/);
  const rail = page.slice(page.indexOf('class="observability-rail"'), page.indexOf("</aside>", page.indexOf('class="observability-rail"')));
  assert.ok(rail.includes('id="rail-back"'), "the way back is in the rail");
});

test("the rail's heading can take focus when View query arrives, and is not in the tab order", () => {
  const [tag] = tagsWithID("telemetry-heading");
  assert.ok(tag.startsWith("<h2 "), tag);
  assert.equal(attr(tag, "tabindex"), "-1");
});

test("the strip and the way back show only where the rail has dropped below the grid", () => {
  const { blocks, topLevel } = mediaBlocks();
  for (const selector of [".search-strip", ".rail-back"]) {
    const base = declarationsFor(selector, topLevel);
    assert.ok(base.some((body) => /display:\s*none/.test(body)), `${selector} is not shown by default`);
    const narrow = blocks.find(([query]) => query === "(max-width: 1050px)");
    assert.ok(narrow, "no (max-width: 1050px) block");
    assert.ok(declarationsFor(selector, narrow[1]).some((body) => /display:\s*(flex|block|inline-flex)/.test(body)), `${selector} shows at 1050px and below`);
  }
});

test("the rail still stops being beside the grid at 1050px, which is where the strip starts", () => {
  const { blocks } = mediaBlocks();
  const drop = blocks.find(([query]) => query === "(max-width: 1050px) and (min-width: 769px)");
  assert.ok(drop, "the breakpoint the strip follows has moved");
  assert.match(drop[1], /\.observability-rail\s*\{[^}]*position:\s*static/);
});

test("the strip is one line that does not wrap, so it keeps one height before, during and after a search", () => {
  const rules = declarationsFor(".search-strip", mediaBlocks().blocks.find(([query]) => query === "(max-width: 1050px)")[1]).join(";");
  assert.match(rules, /flex-wrap:\s*nowrap/);
  assert.match(rules, /min-width:\s*0|overflow:\s*hidden/);
  for (const selector of [".strip-value", ".strip-count"]) {
    assert.ok(declarationsFor(selector, styles).some((body) => /white-space:\s*nowrap/.test(body)), selector);
  }
});

test("the strip and the way back are hidden on the stats route, as the toolbar is", () => {
  for (const selector of ["body.route-stats .search-strip", "body.route-stats .rail-back"]) {
    assert.ok(declarationsFor(selector, styles).some((body) => /display:\s*none/.test(body)), selector);
  }
});

test("the open filter drawer makes the strip inert along with the rest of the page behind it", () => {
  const list = main.match(/const BEHIND_DRAWER = \[([^\]]*)\]/)?.[1] ?? "";
  assert.match(list, /"\.search-strip"/);
  for (const selector of [".skip-link", ".site-header", "main", ".observability-rail", ".site-footer"]) {
    assert.ok(list.includes(`"${selector}"`), selector);
  }
});

test("main.js wires the strip's two controls and hands the search lifecycle to the rail's module", () => {
  assert.match(main, /\$\("view-query"\)\.addEventListener\("click"/);
  assert.match(main, /\$\("rail-back"\)\.addEventListener\("click"/);
  assert.match(main, /noteSearchLoading\(/);
  assert.match(main, /MutationObserver/);
  assert.doesNotMatch(main, /\.innerHTML/);
});

test("View query opens the query panel, takes focus into the rail and scrolls to it", () => {
  const handler = main.match(/function showQueryRail\(\) \{[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(handler, "no showQueryRail");
  assert.match(handler, /\$\("query-inspector"\)\.open = true/);
  assert.match(handler, /\$\("telemetry-heading"\)\.focus\(\{\s*preventScroll:\s*true\s*\}\)/);
  assert.match(handler, /scrollIntoView\(/);
  assert.doesNotMatch(handler, /behavior:\s*"smooth"/, "the page's reduced-motion rule is scroll-behavior, which a smooth option would override");
});
