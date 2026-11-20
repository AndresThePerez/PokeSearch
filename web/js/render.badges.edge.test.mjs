import test from "node:test";
import assert from "node:assert/strict";
import { cardBranches, badgeLegend, badgeTitle, branchLabel, legendHiddenWhenStale } from "./render.js";
import { BRANCHES } from "./telemetry.js";

// cardBranches reads one card's badges out of a response's matched field.
// Every shape the field can arrive in, against every index a caller can pass.

const SHAPES = [
  undefined,
  null,
  "exact",
  "",
  7,
  Number.NaN,
  true,
  { 0: ["exact"] },
  { length: 1, 0: ["exact"] },
  [],
  ["exact", "text"],
  [null, undefined, 7, {}, "exact"],
  [["exact", "text"], ["prefix"]],
  [[["exact"]], [["text", "prefix"]]],
  [[1, 2, {}, [], null, undefined, true]],
  [["", "", "exact", ""]],
  [["exact", "exact", "text", "exact"]],
  [[" ", "\t", "fuzzy-name"]],
  [["constructor", "__proto__", "toString"]],
];

const INDEXES = [0, 1, 2, 5, 100, -1, 0.5, Number.NaN, "0", "1", "length", undefined, null];

test("a card's badges are always an array of non-empty strings, whatever matched holds", () => {
  for (const matched of SHAPES) {
    for (const index of INDEXES) {
      let branches;
      assert.doesNotThrow(() => { branches = cardBranches(matched, index); }, `${JSON.stringify(matched)} @ ${String(index)}`);
      assert.ok(Array.isArray(branches), `${JSON.stringify(matched)} @ ${String(index)}`);
      for (const name of branches) {
        assert.equal(typeof name, "string", `${JSON.stringify(matched)} @ ${String(index)}`);
        assert.ok(name.length > 0, `${JSON.stringify(matched)} @ ${String(index)}`);
      }
    }
  }
});

test("a matched field that is not a list of lists gives no badges", () => {
  for (const matched of [undefined, null, "exact", 7, true, { 0: ["exact"] }, { length: 1, 0: ["exact"] }, ["exact", "text"]]) {
    for (const index of [0, 1]) assert.deepStrictEqual(cardBranches(matched, index), [], JSON.stringify(matched));
  }
  assert.deepStrictEqual(cardBranches([[["exact"]]], 0), [], "a list nested one level too deep is not a list of names");
});

test("an index outside the cards, or not an index at all, gives no badges", () => {
  const matched = [["exact"], ["text"]];
  for (const index of [2, 100, -1, 0.5, Number.NaN, "length", undefined, null]) {
    assert.deepStrictEqual(cardBranches(matched, index), [], String(index));
  }
  assert.deepStrictEqual(cardBranches(matched, 1), ["text"]);
});

test("a card's badges keep the server's order and drop only what is not a name", () => {
  assert.deepStrictEqual(cardBranches([["text", 5, "exact", "", null, "prefix"]], 0), ["text", "exact", "prefix"]);
  assert.deepStrictEqual(cardBranches([["exact", "exact"]], 0), ["exact", "exact"], "a repeat is the server's to send");
});

test("the badges handed back are a copy, so changing them leaves the response as it was", () => {
  const row = ["exact", "text"];
  const matched = [row];
  const branches = cardBranches(matched, 0);
  assert.ok(branches !== row, "not the response's own array");
  branches.push("prefix");
  branches[0] = "changed";
  assert.deepStrictEqual(row, ["exact", "text"]);
  assert.ok(matched[0] === row, "the response still holds its own array");
  assert.deepStrictEqual(cardBranches(matched, 0), ["exact", "text"]);
});

test("the legend names only what a card's badges can show", () => {
  for (const matched of SHAPES) {
    const shown = new Set(
      (Array.isArray(matched) ? matched : []).flatMap((_, index) => cardBranches(matched, index)),
    );
    const legend = badgeLegend(matched);
    if (shown.size === 0) {
      assert.deepStrictEqual(legend, [], JSON.stringify(matched));
      continue;
    }
    for (const { name } of legend) {
      assert.ok(Object.hasOwn(BRANCHES, name) || shown.has(name), `${JSON.stringify(matched)}: ${name}`);
    }
  }
});

// branchLabel and badgeTitle with names the page has no words for.

test("a name the page does not know is always shown as the same string", () => {
  const names = [
    "constructor", "__proto__", "toString", "valueOf", "hasOwnProperty", "isPrototypeOf",
    "__defineGetter__", "propertyIsEnumerable", "toLocaleString",
    "0", "123", "-1", "1e3", "x".repeat(500), "<script>alert(1)</script>", "\" onmouseover=\"x", "ä — ✓",
  ];
  for (const name of names) {
    assert.equal(branchLabel(name), name, name.slice(0, 30));
    const title = badgeTitle(name);
    assert.equal(typeof title, "string");
    assert.ok(title.startsWith(`${name}: `), name.slice(0, 30));
    assert.doesNotMatch(title, /function|native code|\[object|undefined/, name.slice(0, 30));
    const entry = badgeLegend([[name]]).at(-1);
    assert.equal(entry.label, name, name.slice(0, 30));
    assert.equal(typeof entry.meaning, "string");
  }
});

test("the known branches keep their own words", () => {
  for (const [name, { label, meaning }] of Object.entries(BRANCHES)) {
    assert.equal(branchLabel(name), label);
    assert.equal(badgeTitle(name), `${label}: ${meaning}`);
  }
  assert.equal(badgeTitle("fuzzy-name"), "fuzzy: name, allowing a typo");
});

// legendHiddenWhenStale: every combination of a stale grid and the badged
// cards left on it.

test("the legend goes only when the search ended badly and no badged card is left", () => {
  const cases = [
    [{ stale: true, badgedCards: 0 }, true],
    [{ stale: true, badgedCards: undefined }, true],
    [{ stale: true, badgedCards: Number.NaN }, true],
    [{ stale: true, badgedCards: -3 }, true],
    [{ stale: true, badgedCards: 1 }, false],
    [{ stale: true, badgedCards: 24 }, false],
    [{ stale: false, badgedCards: 0 }, false],
    [{ stale: false, badgedCards: 24 }, false],
    [{ stale: undefined, badgedCards: 0 }, false],
    [{}, false],
  ];
  for (const [input, hidden] of cases) {
    assert.equal(legendHiddenWhenStale(input), hidden, JSON.stringify(input));
    assert.equal(typeof legendHiddenWhenStale(input), "boolean");
  }
});
