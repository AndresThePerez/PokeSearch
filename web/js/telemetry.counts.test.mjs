import test from "node:test";
import assert from "node:assert/strict";
import { archiveCountLine, setCodeAndNumber, labWeights } from "./telemetry.js";

const n = (value) => value.toLocaleString();

// archiveCountLine: the header's counts, read from the server

test("the counts the server gave are shown as the page formats numbers", () => {
  const line = archiveCountLine({ cards: 20635, sets: 176 });
  assert.equal(line.cards, `${n(20635)} English cards`);
  assert.equal(line.sets, "176 sets · 1999—today");
  assert.equal(line.placeholder, `Search ${n(20635)} cards…`);
});

test("one card and one set are singular", () => {
  const line = archiveCountLine({ cards: 1, sets: 1 });
  assert.equal(line.cards, "1 English card");
  assert.equal(line.sets, "1 set · 1999—today");
  assert.equal(line.placeholder, "Search 1 card…");
});

test("while loading, and after a failed request, the line says no number at all", () => {
  for (const counts of [{}, { cards: undefined, sets: undefined }, { cards: null, sets: null }]) {
    const line = archiveCountLine(counts);
    assert.equal(line.cards, "English cards");
    assert.equal(line.sets, "1999—today");
    assert.equal(line.placeholder, "Search cards…");
  }
});

test("each count falls back on its own", () => {
  assert.deepStrictEqual(archiveCountLine({ cards: 20635, sets: null }), {
    cards: `${n(20635)} English cards`, sets: "1999—today", placeholder: `Search ${n(20635)} cards…`,
  });
  assert.deepStrictEqual(archiveCountLine({ cards: null, sets: 176 }), {
    cards: "English cards", sets: "176 sets · 1999—today", placeholder: "Search cards…",
  });
});

test("a value that is not a count never reaches the page", () => {
  for (const bad of [Number.NaN, -1, 2.5, Infinity, "20635", "", true, {}, []]) {
    const line = archiveCountLine({ cards: bad, sets: bad });
    for (const text of Object.values(line)) {
      assert.doesNotMatch(text, /NaN|undefined|null|Infinity|object|true|-1|2\.5|20635/, `${String(bad)}: ${text}`);
    }
    assert.equal(line.cards, "English cards", String(bad));
  }
});

test("an empty archive is a count too", () => {
  assert.equal(archiveCountLine({ cards: 0, sets: 0 }).cards, "0 English cards");
});

// setCodeAndNumber: a card id split on its last dash

test("a card id splits into set code and number on its last dash", () => {
  assert.deepStrictEqual(setCodeAndNumber("base1-4"), { set: "base1", number: "4" });
  assert.deepStrictEqual(setCodeAndNumber("cel25c-17_A"), { set: "cel25c", number: "17_A" });
  assert.deepStrictEqual(setCodeAndNumber("ex10-?"), { set: "ex10", number: "?" });
  assert.deepStrictEqual(setCodeAndNumber("sv3pt5-1-2"), { set: "sv3pt5-1", number: "2" });
});

test("an id with no dash is all set code and no number", () => {
  assert.deepStrictEqual(setCodeAndNumber("promo"), { set: "promo", number: "" });
  assert.deepStrictEqual(setCodeAndNumber(""), { set: "", number: "" });
  assert.deepStrictEqual(setCodeAndNumber(undefined), { set: "", number: "" });
});

// labWeights: the lab's weights, each with the badge word it boosts

test("each weight is labelled with the branch it boosts", () => {
  assert.deepStrictEqual(
    labWeights({ exact: 8, prefix: 2, "fuzzy-name": 1.5, text: 1 }),
    ["exact ×8", "prefix ×2", "fuzzy ×1.5"],
  );
});

test("a weight the profile does not carry shows a dash, not a number", () => {
  assert.deepStrictEqual(labWeights({ exact: 8 }), ["exact ×8", "prefix —", "fuzzy —"]);
  assert.deepStrictEqual(labWeights(undefined), ["exact —", "prefix —", "fuzzy —"]);
});
