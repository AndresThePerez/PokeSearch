import test from "node:test";
import assert from "node:assert/strict";
import { countLineText } from "./render.js";
import { isCount, archiveCountLine } from "./telemetry.js";

const n = (value) => value.toLocaleString();

// isCount: the one check both the header and the count line ask.

const COUNTS = [0, -0, 1, 2, 20635, 9999999, Number.MAX_SAFE_INTEGER, 2 ** 53, 1e21];
const NOT_COUNTS = [
  0.5, -1, -20635, Number.NaN, Infinity, -Infinity, "108", "", "0", null, undefined, true, false,
  [], [5], {}, 5n, Object(5), Object(0),
];

test("a count is a whole number of zero or more, and nothing else is", () => {
  for (const value of COUNTS) assert.equal(isCount(value), true, String(value));
  for (const value of NOT_COUNTS) assert.equal(isCount(value), false, typeof value === "bigint" ? "5n" : String(value));
});

// countLineText "done": what an answered search writes.

test("an answered search writes a count as a count, zero as 0, with the page's separators", () => {
  assert.equal(countLineText({ phase: "done", total: 0 }), "0 cards");
  assert.equal(countLineText({ phase: "done", total: -0 }), "0 cards");
  assert.equal(countLineText({ phase: "done", total: 1 }), "1 card");
  assert.equal(countLineText({ phase: "done", total: 2 }), "2 cards");
  for (const total of [20635, 9999999, Number.MAX_SAFE_INTEGER, 2 ** 53, 1e21]) {
    const line = countLineText({ phase: "done", total, current: "Loading cards…" });
    assert.equal(line, `${n(total)} cards`, String(total));
    assert.doesNotMatch(line, /e\+/i);
  }
});

test("as the page asks it, with no earlier text, a total that is not a count shows a dash", () => {
  for (const total of NOT_COUNTS) {
    assert.equal(countLineText({ phase: "done", total }), "—", typeof total === "bigint" ? "5n" : String(total));
    assert.equal(countLineText({ phase: "done", total, current: "" }), "—");
  }
});

test("a total that is not a count is never formatted, whatever the line said before", () => {
  for (const total of NOT_COUNTS) {
    for (const current of ["108 cards", "—"]) {
      const line = countLineText({ phase: "done", total, current });
      assert.equal(line, current);
      assert.doesNotMatch(line, /NaN|undefined|null|-\d|∞|Infinity|\[object/);
    }
  }
});

// The header reads the same check: exporting it changed nothing there.
test("the header's counts read as they did before the check was shared", () => {
  assert.deepStrictEqual(archiveCountLine({ cards: 20635, sets: 176 }), {
    cards: `${n(20635)} English cards`, sets: "176 sets · 1999—today", placeholder: `Search ${n(20635)} cards…`,
  });
  assert.deepStrictEqual(archiveCountLine({ cards: -0, sets: -0 }), {
    cards: "0 English cards", sets: "0 sets · 1999—today", placeholder: "Search 0 cards…",
  });
  for (const bad of NOT_COUNTS) {
    assert.deepStrictEqual(archiveCountLine({ cards: bad, sets: bad }), {
      cards: "English cards", sets: "1999—today", placeholder: "Search cards…",
    }, typeof bad === "bigint" ? "5n" : String(bad));
  }
});
