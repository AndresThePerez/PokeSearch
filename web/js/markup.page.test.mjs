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

const body = page.slice(page.indexOf("</head>"));

function tagsWithID(id) {
  return [...page.matchAll(new RegExp(`<[a-z0-9]+\\b[^>]*\\bid="${id}"[^>]*>`, "g"))].map((match) => match[0]);
}

test("the page body carries no hard-coded card or set count, and no availability figure", () => {
  assert.doesNotMatch(body, /20,635|176 sets/);
  assert.doesNotMatch(page, /99\.9/);
  assert.doesNotMatch(body, /monthly target/);
});

test("the elements the counts, the landing and the legend are painted into exist once each", () => {
  for (const id of ["archive-cards", "archive-sets", "landing", "landing-examples", "empty-examples", "badge-legend"]) {
    assert.equal(tagsWithID(id).length, 1, id);
  }
});

test("the landing sits above the grid, and the empty state's examples come after its message", () => {
  assert.ok(page.indexOf('id="landing"') < page.indexOf('id="results-grid"'));
  assert.ok(page.indexOf('id="badge-legend"') < page.indexOf('id="results-grid"'));
  const empty = page.slice(page.indexOf('id="empty-state"'), page.indexOf('id="load-more"'));
  assert.ok(empty.indexOf('id="empty-message"') !== -1 && empty.indexOf('id="empty-message"') < empty.indexOf('id="empty-examples"'));
});

test("the landing and the legend are hidden on the stats route, as the grid is", () => {
  for (const selector of ["body.route-stats #landing", "body.route-stats #badge-legend"]) {
    assert.ok(declarationsFor(selector).some((rule) => /display:\s*none/.test(rule)), selector);
  }
});

test("the explanation and source links open in a new tab, say so, and pass nothing to the page they open", () => {
  const links = [...body.matchAll(/<a\b[^>]*>[\s\S]*?<\/a>/g)].map((match) => match[0]);
  const relevance = links.filter((a) => a.includes('href="https://github.com/AndresThePerez/pokesearch/blob/master/docs/RELEVANCE.md"'));
  const source = links.filter((a) => a.includes('href="https://github.com/AndresThePerez/pokesearch"'));
  assert.ok(relevance.length >= 2, "near the intro and in the empty state");
  assert.ok(source.length >= 1);
  for (const a of [...relevance, ...source]) {
    assert.match(a, /target="_blank"/, a);
    assert.match(a, /rel="noopener noreferrer"/, a);
    assert.match(a, /<span class="sr-only">\s*\(opens in a new tab\)<\/span>/, a);
  }
  const empty = page.slice(page.indexOf('id="empty-state"'), page.indexOf('id="load-more"'));
  assert.match(empty, /docs\/RELEVANCE\.md/);
});

test("the page ends with a footer that says it is an unofficial fan project and whose marks these are", () => {
  const footer = body.match(/<footer\b[^>]*>([\s\S]*?)<\/footer>/);
  assert.ok(footer, "no footer");
  const text = footer[1].replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");
  assert.match(text, /unofficial fan project/);
  for (const owner of ["Nintendo", "The Pokémon Company", "Game Freak", "Creatures"]) assert.match(text, new RegExp(owner), owner);
  assert.match(text, /not affiliated with or endorsed by/);
  assert.match(text, /trademarks/);
  assert.match(text, /pokemon-tcg-data/);
  assert.doesNotMatch(text, /licen[cs]e/i, "the README states no licence for the data");
  assert.ok(body.indexOf("<footer") > body.indexOf("</main>"));
});

test("the count line starts short, so the grid under it does not move when the count arrives", () => {
  const tag = page.match(/<p id="total-count"[^>]*>([^<]*)<\/p>/);
  assert.ok(tag, "no count line");
  assert.ok(tag[1].length <= 16, tag[1]);
  assert.ok(declarationsFor("#total-count", mediaBlocks().topLevel).some((rule) => /white-space:\s*nowrap/.test(rule)), "it never wraps to a second line");
});
