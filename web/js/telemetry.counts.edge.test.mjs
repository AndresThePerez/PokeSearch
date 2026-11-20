import test from "node:test";
import assert from "node:assert/strict";
import { archiveCountLine, setCodeAndNumber, labWeights } from "./telemetry.js";

// setCodeAndNumber: a card id is "<set>-<number>", split on its last dash.

const IDS = [
  ["base1-4", "base1", "4"],
  ["cel25c-17_A", "cel25c", "17_A"],
  ["swsh12pt5gg-GG01", "swsh12pt5gg", "GG01"],
  ["ex10-?", "ex10", "?"],
  ["sv3pt5-1-2", "sv3pt5-1", "2"],
  ["abc-", "abc", ""],
  ["-17", "", "17"],
  ["-", "", ""],
  ["---", "--", ""],
  ["<b>x</b>-\"1'", "<b>x</b>", "\"1'"],
  ["pokémon-ⅰ", "pokémon", "ⅰ"],
  ["promo", "promo", ""],
  ["", "", ""],
];

test("a card id splits on its last dash, whatever else it holds", () => {
  for (const [id, set, number] of IDS) {
    assert.deepStrictEqual(setCodeAndNumber(id), { set, number }, id);
  }
  const long = `${"a".repeat(300)}-${"9".repeat(300)}`;
  assert.deepStrictEqual(setCodeAndNumber(long), { set: "a".repeat(300), number: "9".repeat(300) });
});

test("the two parts put back together with a dash are the id again", () => {
  for (const [id] of IDS) {
    if (!id.includes("-")) continue;
    const { set, number } = setCodeAndNumber(id);
    assert.equal(`${set}-${number}`, id);
  }
});

test("an id that is not a string gives two empty strings and never throws", () => {
  for (const id of [undefined, null, 0, 42, -1, Number.NaN, true, {}, [], ["base1-4"], { toString: () => "x-1" }, Symbol("id")]) {
    let parts;
    assert.doesNotThrow(() => { parts = setCodeAndNumber(id); }, typeof id);
    assert.equal(typeof parts.set, "string");
    assert.equal(typeof parts.number, "string");
    assert.deepStrictEqual(parts, { set: "", number: "" });
  }
});

// archiveCountLine: what the header says about the archive's size.

const hasDigit = (text) => /\d/.test(text);

test("zero, written as JSON can write it, is shown as 0 and never with a minus sign", () => {
  // JSON.parse("-0") is -0, a whole number and not below zero.
  const fromJSON = JSON.parse('{"docs":-0,"sets":-0}');
  const line = archiveCountLine({ cards: fromJSON.docs, sets: fromJSON.sets });
  for (const text of Object.values(line)) assert.doesNotMatch(text, /-\s*0|−/, text);
  assert.equal(line.cards, archiveCountLine({ cards: 0, sets: 0 }).cards);
  assert.equal(line.sets, archiveCountLine({ cards: 0, sets: 0 }).sets);
});

test("large counts are formatted with the page's separators, never in exponent form", () => {
  for (const count of [9999999, 1234567890, Number.MAX_SAFE_INTEGER, 1e21]) {
    const line = archiveCountLine({ cards: count, sets: count });
    assert.equal(line.cards, `${count.toLocaleString()} English cards`);
    assert.equal(line.sets, `${count.toLocaleString()} sets · 1999—today`);
    assert.doesNotMatch(Object.values(line).join(" "), /e\+|E\+/);
  }
});

test("only a valid card count puts a number in the search box, and each count stands alone", () => {
  const bad = [undefined, null, -1, -0.5, 0.5, 20635.1, Number.NaN, Infinity, -Infinity, "20635", "", "0", true, false, {}, [], [20635]];
  for (const cards of bad) {
    for (const sets of [176, ...bad]) {
      const line = archiveCountLine({ cards, sets });
      assert.equal(line.cards, "English cards", `cards=${String(cards)}`);
      assert.equal(line.placeholder, "Search cards…", `cards=${String(cards)}`);
      assert.ok(!hasDigit(line.cards) && !hasDigit(line.placeholder));
      if (sets === 176) assert.equal(line.sets, "176 sets · 1999—today");
    }
  }
  for (const sets of bad) {
    const line = archiveCountLine({ cards: 20635, sets });
    assert.equal(line.sets, "1999—today", `sets=${String(sets)}`);
    assert.equal(line.cards, `${(20635).toLocaleString()} English cards`);
    assert.equal(line.placeholder, `Search ${(20635).toLocaleString()} cards…`);
  }
});

// labWeights: three labelled weights, in badge order.

test("the lab's weights are the three name branches in badge order, a text boost never among them", () => {
  const weights = labWeights({ text: 3, "fuzzy-name": 1.25, prefix: 2.0, exact: 8, phonetic: 9 });
  assert.deepStrictEqual(weights, ["exact ×8", "prefix ×2", "fuzzy ×1.25"]);
  assert.deepStrictEqual(labWeights({}), ["exact —", "prefix —", "fuzzy —"]);
  assert.deepStrictEqual(labWeights({ exact: 0 }), ["exact ×0", "prefix —", "fuzzy —"], "zero is a weight, not a missing one");
});
