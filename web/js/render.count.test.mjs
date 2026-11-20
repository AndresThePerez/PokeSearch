import test from "node:test";
import assert from "node:assert/strict";
import { countLineText } from "./render.js";

const n = (value) => value.toLocaleString();

// countLineText: what the count line above the grid says, as a search starts
// and ends

test("while a search is on its way the line keeps what it says", () => {
  assert.equal(countLineText({ phase: "loading", current: "Loading cards…" }), "Loading cards…", "the first search");
  assert.equal(countLineText({ phase: "loading", current: "108 cards" }), "108 cards", "a later one");
});

test("a search that answered writes its total", () => {
  assert.equal(countLineText({ phase: "done", total: 20635, current: "Loading cards…" }), `${n(20635)} cards`);
  assert.equal(countLineText({ phase: "done", total: 1, current: "—" }), "1 card");
  assert.equal(countLineText({ phase: "done", total: 0, current: "108 cards" }), "0 cards");
});

test("a search that failed or was refused with no card on screen leaves a dash, never the loading text", () => {
  assert.equal(countLineText({ phase: "ended", cardsShown: 0, current: "Loading cards…" }), "—");
  assert.equal(countLineText({ phase: "ended", cardsShown: 0, current: "108 cards" }), "—");
});

test("a search that failed or was refused over earlier results keeps the line as it was", () => {
  assert.equal(countLineText({ phase: "ended", cardsShown: 24, current: "108 cards" }), "108 cards");
  assert.equal(countLineText({ phase: "ended", cardsShown: 24, current: "—" }), "—");
});
