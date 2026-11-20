import test from "node:test";
import assert from "node:assert/strict";
import {
  EXAMPLE_QUERIES,
  examplePlacement,
  badgeLegend,
  badgeTitle,
  branchLabel,
  legendHiddenWhenStale,
  cardBranches,
} from "./render.js";
import { defaults } from "./state.js";

const withState = (fields) => ({ ...defaults, types: [], ...fields });

// EXAMPLE_QUERIES: the searches the landing and the empty state offer

test("there are five example searches, each a different query with words saying what it shows", () => {
  assert.equal(EXAMPLE_QUERIES.length, 5);
  assert.deepStrictEqual(
    EXAMPLE_QUERIES.map((example) => example.q),
    ["charizard", "pika", "pikuchu", "flip a coin", "Mitsuhiro Arita"],
  );
  const shows = EXAMPLE_QUERIES.map((example) => example.shows);
  assert.equal(new Set(shows).size, 5, "each says something different");
  for (const words of shows) {
    assert.equal(typeof words, "string");
    assert.ok(words.trim().length > 0 && words.length <= 30, words);
  }
});

test("the example list cannot be changed from outside", () => {
  assert.ok(Object.isFrozen(EXAMPLE_QUERIES));
  for (const example of EXAMPLE_QUERIES) assert.ok(Object.isFrozen(example), example.q);
});

// examplePlacement: where the examples go, if anywhere

test("a page with no query and no filters is the landing, before and after its results land", () => {
  assert.equal(examplePlacement({ state: withState({}), total: undefined }), "landing");
  assert.equal(examplePlacement({ state: withState({}), total: 20635 }), "landing");
  assert.equal(examplePlacement({ state: withState({ q: "   " }), total: 20635 }), "landing", "blank text is no query");
  assert.equal(examplePlacement({ state: withState({ sort: "hp" }), total: 20635 }), "landing", "a sort is not a filter");
});

test("a search that found nothing offers the examples under its message", () => {
  assert.equal(examplePlacement({ state: withState({ q: "zzzzqqqq" }), total: 0 }), "empty");
  assert.equal(examplePlacement({ state: withState({ set: "base1", rarity: "Rare" }), total: 0 }), "empty");
  assert.equal(examplePlacement({ state: withState({ q: "pika", types: ["Water"] }), total: 0 }), "empty");
});

test("a page with a query or filters and results shows no examples", () => {
  for (const fields of [{ q: "charizard" }, { types: ["Fire"] }, { supertype: "trainer" }, { set: "base1" }, { rarity: "Rare" }, { series: "Base" }, { q: "pika", set: "base1" }]) {
    assert.equal(examplePlacement({ state: withState(fields), total: 12 }), "none", JSON.stringify(fields));
  }
});

test("a page with a query or filters waiting on its answer shows no examples yet", () => {
  assert.equal(examplePlacement({ state: withState({ q: "charizard" }), total: undefined }), "none");
  assert.equal(examplePlacement({ state: withState({ types: ["Fire"] }), total: undefined }), "none");
});

// badgeLegend and badgeTitle: what the match badges mean, from one table

test("results without badges get no legend", () => {
  assert.deepStrictEqual(badgeLegend(undefined), []);
  assert.deepStrictEqual(badgeLegend([]), []);
  assert.deepStrictEqual(badgeLegend([[], []]), []);
});

test("results with badges get every branch the page knows, in badge order, each under its badge's own label", () => {
  const legend = badgeLegend([["text"], ["prefix", "text"]]);
  assert.deepStrictEqual(legend.map((entry) => entry.name), ["exact", "prefix", "fuzzy-name", "text"]);
  for (const entry of legend) {
    assert.equal(entry.label, branchLabel(entry.name));
    assert.ok(entry.meaning.length > 0, entry.name);
  }
});

test("a branch the page has no words for is listed after the known ones, under its own name", () => {
  const legend = badgeLegend([["exact", "phonetic"], ["phonetic", "text"], ["new-one"]]);
  assert.deepStrictEqual(legend.map((entry) => entry.label), ["exact", "prefix", "fuzzy", "text", "phonetic", "new-one"]);
  assert.match(legend[4].meaning, /phonetic/);
});

test("a badge's tooltip says the same words as its legend entry", () => {
  for (const entry of badgeLegend([["exact", "prefix", "fuzzy-name", "text", "phonetic"]])) {
    assert.equal(badgeTitle(entry.name), `${entry.label}: ${entry.meaning}`);
  }
});

test("each known branch means something different", () => {
  const meanings = badgeLegend([["exact"]]).map((entry) => entry.meaning);
  assert.equal(new Set(meanings).size, 4);
});

// legendHiddenWhenStale: the legend after a search that failed or was refused

test("after a failed or refused search the legend goes when no card on screen carries a badge", () => {
  assert.equal(legendHiddenWhenStale({ stale: true, badgedCards: 0 }), true);
});

test("the legend stays while badged cards from the last search are still on screen, and on any search that worked", () => {
  assert.equal(legendHiddenWhenStale({ stale: true, badgedCards: 12 }), false);
  assert.equal(legendHiddenWhenStale({ stale: false, badgedCards: 0 }), false);
  assert.equal(legendHiddenWhenStale({ stale: false, badgedCards: 24 }), false);
});

// cardBranches: one card's badges, read from a response's matched field

test("a card's branches are its entry in matched, in order", () => {
  assert.deepStrictEqual(cardBranches([["exact", "text"], ["prefix"]], 0), ["exact", "text"]);
  assert.deepStrictEqual(cardBranches([["exact", "text"], ["prefix"]], 1), ["prefix"]);
  assert.deepStrictEqual(cardBranches([["exact"]], 5), [], "a card past the end has none");
});

test("a matched field of the wrong shape gives a card no badges, and only non-empty strings are badges", () => {
  for (const matched of [undefined, null, "exact", 5, { 0: ["exact"] }, [null], ["exact"], [{}]]) {
    assert.deepStrictEqual(cardBranches(matched, 0), [], JSON.stringify(matched));
  }
  assert.deepStrictEqual(cardBranches([["exact", "", null, 42, "text"]], 0), ["exact", "text"]);
});
