import test from "node:test";
import assert from "node:assert/strict";
import { searchStripView, setStripCompact } from "./telemetry.js";

// The strip's short form on a narrow screen, held to the same hostile inputs
// as the full form: the count drops its noun and nothing else changes.

const view = (input) => searchStripView(input);
const BLANK = "—";
const STATES = ["none", "loading", "done", "failed", "bogus", undefined];
const NUMBERS = [0, -0, 0.4, 1, 1.5, 2345, 20635, 9999999, 1e21, -1, NaN, Infinity, "12", null, undefined, true, [], {}];
const TOTALS = [0, -0, 1, 2, 24, 999, 1000, 20635, 9999999, 2 ** 53, 1.5, -1, NaN, Infinity, "9", null, undefined, {}];
const broken = /NaN|undefined|null|Infinity|\[object|function|true|false|-\s?\d|−|-0/;

test("a compact strip prints no broken figure for any state, time or total", () => {
  for (const state of STATES) {
    for (const esMs of NUMBERS) {
      for (const total of TOTALS) {
        const v = view({ state, esMs, roundTripMs: esMs, total, compact: true });
        for (const item of [v.es, v.roundTrip, v.count, v.action]) {
          for (const text of [item.text, item.name]) {
            assert.doesNotMatch(text, broken, `${String(state)} ${String(esMs)} ${String(total)}: ${text}`);
          }
        }
      }
    }
  }
});

test("the compact count is the bare number, and the name keeps the noun in the right number", () => {
  for (const total of [0, -0, 1, 2, 24, 999, 1000, 20635, 9999999]) {
    const v = view({ state: "done", esMs: 1, roundTripMs: 1, total, compact: true });
    const bare = (total === 0 ? 0 : total).toLocaleString();
    assert.equal(v.count.text, bare, String(total));
    assert.equal(v.count.name, `Result count: ${bare} ${total === 1 ? "card" : "cards"}`, String(total));
    assert.doesNotMatch(v.count.text, /card/i);
  }
});

test("compact changes the count's text and nothing else, in every state", () => {
  for (const state of STATES) {
    for (const esMs of NUMBERS) {
      for (const total of TOTALS) {
        const input = { state, esMs, roundTripMs: esMs, total };
        const full = view(input);
        const small = view({ ...input, compact: true });
        const where = `${String(state)} ${String(esMs)} ${String(total)}`;
        assert.deepStrictEqual(small.es, full.es, where);
        assert.deepStrictEqual(small.roundTrip, full.roundTrip, where);
        assert.deepStrictEqual(small.action, full.action, where);
        assert.equal(small.busy, full.busy, where);
        assert.equal(small.count.name, full.count.name, where);
        assert.equal(small.count.text, full.count.text.replace(/ cards?$/, ""), where);
      }
    }
  }
});

test("a dash stays a dash when compact, and its reason is unchanged", () => {
  for (const state of ["none", "loading", "failed"]) {
    const v = view({ state, compact: true });
    assert.equal(v.count.text, BLANK);
    assert.equal(v.count.name, view({ state }).count.name);
  }
  for (const total of [NaN, null, undefined, -1, 1.5, "24", {}]) {
    const v = view({ state: "done", total, compact: true });
    assert.equal(v.count.text, BLANK, String(total));
    assert.match(v.count.name, /not available/);
  }
});

test("times are never shortened by compact, even at the widest the page can show", () => {
  const v = view({ state: "done", esMs: 12345, roundTripMs: 12345.4, total: 9999999, compact: true });
  assert.equal(v.es.text, "12345 ms");
  assert.equal(v.roundTrip.text, "12345 ms");
  assert.equal(v.es.name, "Elasticsearch time: 12345 milliseconds");
});

test("a flag that is not true leaves the whole count, whatever else it is", () => {
  for (const compact of [false, undefined, null, 0, NaN, ""]) {
    assert.equal(view({ state: "done", total: 24, compact }).count.text, "24 cards", String(compact));
  }
  // The page only ever passes a boolean. Any other value must still give one
  // of the two forms, never a garbled one.
  for (const compact of [1, "yes", {}, [], "false"]) {
    const text = view({ state: "done", total: 24, compact }).count.text;
    assert.ok(text === "24" || text === "24 cards", `${JSON.stringify(compact)}: ${text}`);
  }
});

test("each call returns fresh objects, with compact or without", () => {
  const a = view({ state: "done", total: 5, compact: true });
  const b = view({ state: "done", total: 5, compact: true });
  assert.ok(a !== b && a.count !== b.count && a.action !== b.action);
  a.count.text = "changed";
  assert.equal(view({ state: "done", total: 5, compact: true }).count.text, "5");
});

test("the width setter is exported as a function of one flag", () => {
  assert.equal(typeof setStripCompact, "function");
  assert.equal(setStripCompact.length, 1);
});
