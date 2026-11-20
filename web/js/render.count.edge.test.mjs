import test from "node:test";
import assert from "node:assert/strict";
import { countLineText } from "./render.js";

const n = (value) => value.toLocaleString();

// countLineText: the count line above the grid, at every point a search can
// be at.

test("an answered search writes its total with the page's separators and the right noun", () => {
  const cases = [
    [0, "0 cards"],
    [1, "1 card"],
    [2, "2 cards"],
    [20635, `${n(20635)} cards`],
    [9999999, `${n(9999999)} cards`],
  ];
  for (const [total, line] of cases) {
    assert.equal(countLineText({ phase: "done", total, current: "Loading cards…" }), line, String(total));
    assert.equal(countLineText({ phase: "done", total, cardsShown: 24, current: "108 cards" }), line, "what was there before does not matter");
  }
});

// The server's total is a whole number of zero or more. A total JSON can
// carry that is not one is never written as a count: no "-0", no minus sign,
// no "NaN" and no "undefined".
test("an answered search never writes a total that is not a count", () => {
  assert.equal(countLineText({ phase: "done", total: JSON.parse("-0") }), "0 cards", "zero is written 0");
  for (const total of [undefined, null, Number.NaN, -1, -20635, "lots", {}, []]) {
    const line = countLineText({ phase: "done", total, current: "108 cards" });
    assert.equal(typeof line, "string");
    assert.doesNotMatch(line, /NaN|undefined|null|\[object|-|−|∞|Infinity/, `${JSON.stringify(total)}: ${line}`);
    assert.notEqual(line, "0 card");
  }
});

test("a search that ended with no card on screen leaves a dash, whatever the line said", () => {
  for (const current of ["Loading cards…", "108 cards", "—", ""]) {
    for (const cardsShown of [0, undefined, null, Number.NaN, -1]) {
      assert.equal(countLineText({ phase: "ended", cardsShown, current }), "—", `${current} / ${String(cardsShown)}`);
    }
  }
  assert.equal(countLineText({ phase: "ended" }), "—", "nothing passed but the phase");
});

test("a search that ended over cards still shown keeps the line those cards were counted by", () => {
  for (const current of ["108 cards", "1 card", "—"]) {
    for (const cardsShown of [1, 24, 96]) {
      assert.equal(countLineText({ phase: "ended", cardsShown, current }), current);
    }
  }
});

test("a search on its way, or a phase the page does not name, changes nothing", () => {
  for (const phase of ["loading", "", "unknown", undefined, "DONE", "Ended"]) {
    for (const current of ["Loading cards…", "108 cards", "—"]) {
      assert.equal(countLineText({ phase, total: 5, cardsShown: 0, current }), current, `${String(phase)} / ${current}`);
    }
  }
});

test("the line never goes back to saying it is loading once a search has ended or answered", () => {
  for (const phase of ["ended", "done"]) {
    for (const cardsShown of [0, 24]) {
      const line = countLineText({ phase, total: 3, cardsShown, current: cardsShown ? "3 cards" : "Loading cards…" });
      assert.doesNotMatch(line, /Loading/, `${phase} / ${cardsShown}`);
    }
  }
});
