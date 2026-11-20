import test from "node:test";
import assert from "node:assert/strict";
import { searchStripView } from "./telemetry.js";

// searchStripView under hostile input: every state, every kind of number the
// page could be handed, and the rules that hold across all of them.

const view = (input) => searchStripView(input);
const BLANK = "—";
const STATES = ["none", "loading", "done", "failed"];
const NUMBERS = [
  0, -0, 0.04, 0.4, 0.5, 1, 1.4, 1.5, 2, 99.5, 999.5, 2345, 9999999, 1e21, -1, -0.4,
  NaN, Infinity, -Infinity, "12", "", null, undefined, true, false, [], [5], {}, () => 7,
];
const everyText = (v) => [v.es, v.roundTrip, v.count, v.action].flatMap((item) => [item.text, item.name]);
const label = (value) => (typeof value === "function" ? "fn" : Object.is(value, -0) ? "-0" : JSON.stringify(value) ?? String(value));

test("no state and no number the page could pass ever prints a broken figure", () => {
  const broken = /NaN|undefined|null|Infinity|\[object|function|true|false|-\s?\d|−|-0/;
  for (const state of [...STATES, "bogus", "", undefined, null, 7, {}]) {
    for (const esMs of NUMBERS) {
      for (const roundTripMs of NUMBERS) {
        for (const total of [0, 1, 9999999, 1.5, -1, NaN, "9", null, undefined, {}]) {
          const v = view({ state, esMs, roundTripMs, total });
          for (const text of everyText(v)) {
            assert.doesNotMatch(text, broken, `${String(state)} ${label(esMs)} ${label(roundTripMs)} ${label(total)}: ${text}`);
          }
        }
      }
    }
  }
});

test("a reading's text and its name always agree: a dash has a reason, a figure has none", () => {
  for (const state of STATES) {
    for (const value of NUMBERS) {
      const v = view({ state, esMs: value, roundTripMs: value, total: value });
      for (const reading of [v.es, v.roundTrip, v.count]) {
        if (reading.text === BLANK) {
          assert.match(reading.name, /no search yet|searching|unavailable|not available/, `${state} ${label(value)}: ${reading.name}`);
        } else {
          assert.doesNotMatch(reading.name, /no search yet|searching|unavailable|not available/, `${state} ${label(value)}: ${reading.name}`);
          const figure = (s) => s.match(/[0-9][0-9.,]*(?:e\+[0-9]+)?/)?.[0];
          assert.equal(figure(reading.name.split(": ")[1]), figure(reading.text), `${state} ${label(value)}: ${reading.text} / ${reading.name}`);
        }
      }
    }
  }
});

test("one is singular and everything else plural, in the text and in the name, at the edges of rounding", () => {
  // The round trip is rounded first, so 0.5 and 1.4 are both "1".
  for (const [trip, one] of [[0, false], [0.49, false], [0.5, true], [1, true], [1.49, true], [1.5, false], [2, false]]) {
    const v = view({ state: "done", esMs: 5, roundTripMs: trip, total: 3 });
    assert.equal(v.roundTrip.name, one ? "Round trip: 1 millisecond" : `Round trip: ${Math.round(trip)} milliseconds`, String(trip));
    assert.equal(v.roundTrip.text, `${Math.round(trip)} ms`, String(trip));
  }
  // The Elasticsearch time is printed raw, so only exactly 1 is singular.
  for (const [es, name] of [[1, "1 millisecond"], [1.0, "1 millisecond"], [1.5, "1.5 milliseconds"], [0.5, "0.5 milliseconds"], [0, "0 milliseconds"], [2, "2 milliseconds"]]) {
    assert.equal(view({ state: "done", esMs: es, roundTripMs: 1, total: 3 }).es.name, `Elasticsearch time: ${name}`, String(es));
  }
  for (const [total, text] of [[0, "0 cards"], [1, "1 card"], [2, "2 cards"], [999, "999 cards"]]) {
    const v = view({ state: "done", esMs: 1, roundTripMs: 1, total });
    assert.equal(v.count.text, text);
    assert.equal(v.count.name, `Result count: ${text}`);
  }
});

test("the figures are the ones the rail prints for the same answer", () => {
  // The rail writes `${took_ms} ms`, `${Math.round(roundTrip)} ms` and the total through toLocaleString.
  for (const es of [0, 0.4, 1, 7, 99, 100, 999.5, 2345]) {
    for (const trip of [0, 0.4, 0.5, 15.2, 249.5, 250, 3000, 12344.5]) {
      for (const total of [0, 1, 24, 999, 1000, 20635, 9999999]) {
        const v = view({ state: "done", esMs: es, roundTripMs: trip, total });
        assert.equal(v.es.text, `${es} ms`);
        assert.equal(v.roundTrip.text, `${Math.round(trip)} ms`);
        assert.equal(v.count.text, `${total.toLocaleString()} ${total === 1 ? "card" : "cards"}`);
      }
    }
  }
});

test("a failed or empty strip never shows a number it was handed", () => {
  const handed = { esMs: 4242, roundTripMs: 31337.4, total: 8675309 };
  for (const state of ["failed", "none", "bogus", undefined]) {
    const v = view({ state, ...handed });
    assert.doesNotMatch(JSON.stringify(v), /4242|31337|8675309|8,675,309/, String(state));
    assert.deepStrictEqual([v.es.text, v.roundTrip.text, v.count.text], [BLANK, BLANK, BLANK]);
    assert.equal(v.busy, false);
  }
});

test("a search on its way never invents a number, and keeps only what it was handed", () => {
  const first = view({ state: "loading" });
  assert.deepStrictEqual([first.es.text, first.roundTrip.text, first.count.text], [BLANK, BLANK, BLANK]);
  assert.equal(first.busy, true);
  for (const bad of [NaN, null, -1, "5", {}, Infinity]) {
    const v = view({ state: "loading", esMs: bad, roundTripMs: bad, total: bad });
    assert.deepStrictEqual([v.es.text, v.roundTrip.text, v.count.text], [BLANK, BLANK, BLANK], label(bad));
    assert.equal(v.busy, true);
  }
  // One valid figure among invalid ones stays and the rest are dashes.
  const mixed = view({ state: "loading", esMs: 12, roundTripMs: NaN, total: 24 });
  assert.deepStrictEqual([mixed.es.text, mixed.roundTrip.text, mixed.count.text], ["12 ms", BLANK, "24 cards"]);
});

test("only the exact lowercase state names show figures; any other spelling is the empty strip", () => {
  for (const state of ["DONE", "Done", " done", "done ", "Loading", "LOADING", "failed ", "done\n", "do ne"]) {
    const v = view({ state, esMs: 5, roundTripMs: 5, total: 5 });
    assert.deepStrictEqual(v, view({ state: "none" }), JSON.stringify(state));
  }
});

test("the control is the same in every state and every call, and no two calls share an object", () => {
  const first = view({ state: "done", esMs: 1, roundTripMs: 1, total: 1 });
  const seen = [];
  for (const state of [...STATES, "bogus"]) for (const value of [1, NaN]) {
    const v = view({ state, esMs: value, roundTripMs: value, total: value });
    assert.deepStrictEqual(v.action, first.action);
    for (const part of [v, v.es, v.roundTrip, v.count, v.action]) {
      assert.ok(!seen.includes(part), "an object came back twice");
      seen.push(part);
    }
  }
  first.action.name = "changed";
  first.count.text = "changed";
  const next = view({ state: "done", esMs: 1, roundTripMs: 1, total: 1 });
  assert.equal(next.action.name.startsWith("View query"), true);
  assert.equal(next.count.text, "1 card");
});

test("input that is not an object of figures is the empty strip, not an error", () => {
  assert.deepStrictEqual(view(), view({ state: "none" }));
  assert.deepStrictEqual(view(undefined), view({ state: "none" }));
  assert.deepStrictEqual(view({}), view({ state: "none" }));
  assert.deepStrictEqual(view({ state: "done" }), view({ state: "done", esMs: undefined, roundTripMs: undefined, total: undefined }));
  const done = view({ state: "done" });
  assert.deepStrictEqual([done.es.text, done.roundTrip.text, done.count.text], [BLANK, BLANK, BLANK]);
  for (const part of [done.es, done.roundTrip, done.count]) assert.match(part.name, /not available/);
});
