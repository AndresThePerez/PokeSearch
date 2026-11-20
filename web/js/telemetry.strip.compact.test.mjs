import test from "node:test";
import assert from "node:assert/strict";
import * as telemetry from "./telemetry.js";

// On a phone the strip has about 270px for three figures and a button, so the
// count drops its noun there: the number is what the strip is for, the toolbar
// above it says "cards", and the accessible name keeps the whole phrase.

const view = (input) => telemetry.searchStripView(input);
const n = (value) => value.toLocaleString();

test("a compact strip shows the bare count and keeps the noun in the name", () => {
  const v = view({ state: "done", esMs: 12345, roundTripMs: 12345, total: 9999999, compact: true });
  assert.equal(v.count.text, n(9999999));
  assert.equal(v.count.name, `Result count: ${n(9999999)} cards`);
  assert.equal(v.es.text, "12345 ms", "a time is shown whole: the rail's own figure");
  assert.equal(v.roundTrip.text, "12345 ms");
});

test("a compact count is singular in the name and has no noun in the text, at one and at zero", () => {
  assert.deepStrictEqual(
    [view({ state: "done", total: 1, compact: true }).count.text, view({ state: "done", total: 1, compact: true }).count.name],
    ["1", "Result count: 1 card"],
  );
  assert.deepStrictEqual(
    [view({ state: "done", total: 0, compact: true }).count.text, view({ state: "done", total: -0, compact: true }).count.name],
    ["0", "Result count: 0 cards"],
  );
});

test("without the flag, or with it false, the count carries its noun as before", () => {
  for (const compact of [undefined, false, 0, null]) {
    assert.equal(view({ state: "done", total: 24, compact }).count.text, "24 cards");
  }
});

test("compact changes the count's text and nothing else", () => {
  for (const input of [
    { state: "none" }, { state: "loading" }, { state: "failed", total: 5 },
    { state: "done", esMs: 3, roundTripMs: 40.4, total: 20635 }, { state: "done", esMs: NaN, roundTripMs: -1, total: 1.5 },
  ]) {
    const full = view(input);
    const small = view({ ...input, compact: true });
    assert.deepStrictEqual(small.es, full.es);
    assert.deepStrictEqual(small.roundTrip, full.roundTrip);
    assert.deepStrictEqual(small.action, full.action);
    assert.equal(small.busy, full.busy);
    assert.equal(small.count.name, full.count.name);
    if (full.count.text === "—") assert.equal(small.count.text, "—");
    else assert.equal(small.count.text, full.count.text.replace(/ cards?$/, ""));
  }
});

test("the strip is told when the screen is narrow enough to be compact", () => {
  assert.equal(typeof telemetry.setStripCompact, "function");
});
