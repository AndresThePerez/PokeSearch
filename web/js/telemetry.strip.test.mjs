import test from "node:test";
import assert from "node:assert/strict";
import * as telemetry from "./telemetry.js";

// searchStripView: what the compact strip above the grid says about the last
// search, at the widths where the observability rail sits below the results.

const n = (value) => value.toLocaleString();
const view = (input) => telemetry.searchStripView(input);

const BLANK = "—";

test("before any search the strip has no numbers and says why", () => {
  const v = view({ state: "none" });
  assert.equal(v.es.text, BLANK);
  assert.equal(v.roundTrip.text, BLANK);
  assert.equal(v.count.text, BLANK);
  assert.match(v.es.name, /Elasticsearch time/);
  assert.match(v.es.name, /no search yet/i);
  assert.match(v.roundTrip.name, /Round trip/);
  assert.match(v.count.name, /Result count/);
  assert.equal(v.busy, false);
});

test("a search that answered shows its Elasticsearch time, round trip and count", () => {
  const v = view({ state: "done", esMs: 12, roundTripMs: 142.4, total: 24 });
  assert.equal(v.es.text, "12 ms");
  assert.equal(v.roundTrip.text, "142 ms");
  assert.equal(v.count.text, "24 cards");
  assert.equal(v.es.name, "Elasticsearch time: 12 milliseconds");
  assert.equal(v.roundTrip.name, "Round trip: 142 milliseconds");
  assert.equal(v.count.name, "Result count: 24 cards");
  assert.equal(v.busy, false);
});

test("the round trip is rounded the way the rail rounds it", () => {
  assert.equal(view({ state: "done", esMs: 3, roundTripMs: 99.5, total: 2 }).roundTrip.text, "100 ms");
  assert.equal(view({ state: "done", esMs: 3, roundTripMs: 99.49, total: 2 }).roundTrip.text, "99 ms");
});

test("zero results is a count of zero, not a blank", () => {
  const v = view({ state: "done", esMs: 4, roundTripMs: 60, total: 0 });
  assert.equal(v.count.text, "0 cards");
  assert.equal(v.count.name, "Result count: 0 cards");
});

test("one result is singular", () => {
  const v = view({ state: "done", esMs: 4, roundTripMs: 60, total: 1 });
  assert.equal(v.count.text, "1 card");
  assert.equal(v.count.name, "Result count: 1 card");
});

test("a total past the reachable window is the whole total, formatted as the page formats counts", () => {
  const v = view({ state: "done", esMs: 9, roundTripMs: 80, total: 20635 });
  assert.equal(v.count.text, `${n(20635)} cards`);
  assert.equal(v.count.name, `Result count: ${n(20635)} cards`);
  assert.equal(view({ state: "done", esMs: 9, roundTripMs: 80, total: 1234567 }).count.text, `${n(1234567)} cards`);
});

test("a failed search shows no numbers, even when the last ones are passed in", () => {
  const v = view({ state: "failed", esMs: 12, roundTripMs: 142, total: 24 });
  assert.equal(v.es.text, BLANK);
  assert.equal(v.roundTrip.text, BLANK);
  assert.equal(v.count.text, BLANK);
  assert.match(v.es.name, /unavailable/i);
  assert.match(v.roundTrip.name, /unavailable/i);
  assert.match(v.count.name, /unavailable/i);
  assert.doesNotMatch(JSON.stringify(v), /142|24 cards/);
  assert.equal(v.busy, false);
});

test("times under a millisecond and multi-second times read as the rail reads them", () => {
  const fast = view({ state: "done", esMs: 0, roundTripMs: 0.4, total: 3 });
  assert.equal(fast.es.text, "0 ms");
  assert.equal(fast.roundTrip.text, "0 ms");
  assert.equal(fast.es.name, "Elasticsearch time: 0 milliseconds");
  assert.equal(view({ state: "done", esMs: 0.4, roundTripMs: 5, total: 3 }).es.text, "0.4 ms");
  const slow = view({ state: "done", esMs: 2345, roundTripMs: 6789.6, total: 3 });
  assert.equal(slow.es.text, "2345 ms");
  assert.equal(slow.roundTrip.text, "6790 ms");
  assert.equal(slow.roundTrip.name, "Round trip: 6790 milliseconds");
});

test("one millisecond is singular in the accessible name", () => {
  const v = view({ state: "done", esMs: 1, roundTripMs: 1, total: 3 });
  assert.equal(v.es.name, "Elasticsearch time: 1 millisecond");
  assert.equal(v.roundTrip.name, "Round trip: 1 millisecond");
});

test("a negative zero is written 0, never -0", () => {
  const v = view({ state: "done", esMs: -0, roundTripMs: -0, total: -0 });
  assert.equal(v.es.text, "0 ms");
  assert.equal(v.roundTrip.text, "0 ms");
  assert.equal(v.count.text, "0 cards");
});

test("a value that is not a time, or a total that is not a count, is a dash on its own", () => {
  for (const bad of [undefined, null, NaN, Infinity, -Infinity, -5, "12", {}, []]) {
    const v = view({ state: "done", esMs: bad, roundTripMs: 50, total: 7 });
    assert.equal(v.es.text, BLANK, String(bad));
    assert.equal(v.roundTrip.text, "50 ms");
    assert.equal(v.count.text, "7 cards");
    assert.match(v.es.name, /not available/i);
    const trip = view({ state: "done", esMs: 5, roundTripMs: bad, total: 7 });
    assert.equal(trip.roundTrip.text, BLANK, String(bad));
    assert.equal(trip.es.text, "5 ms");
  }
  for (const bad of [undefined, null, NaN, Infinity, -1, 1.5, "24", {}]) {
    const v = view({ state: "done", esMs: 5, roundTripMs: 50, total: bad });
    assert.equal(v.count.text, BLANK, String(bad));
    assert.match(v.count.name, /not available/i);
    assert.equal(v.es.text, "5 ms");
  }
});

test("while a search is on its way the numbers stay, marked busy, and a first search shows none", () => {
  const first = view({ state: "loading" });
  assert.equal(first.es.text, BLANK);
  assert.equal(first.roundTrip.text, BLANK);
  assert.equal(first.count.text, BLANK);
  assert.equal(first.busy, true);
  const again = view({ state: "loading", esMs: 12, roundTripMs: 142, total: 24 });
  assert.equal(again.es.text, "12 ms");
  assert.equal(again.roundTrip.text, "142 ms");
  assert.equal(again.count.text, "24 cards");
  assert.equal(again.busy, true);
});

test("nothing the strip says is ever NaN, undefined, null or negative zero", () => {
  const values = [undefined, null, NaN, Infinity, -1, -0, 0, 0.4, 1, 12.5, 2345, "7", {}];
  for (const state of ["none", "loading", "done", "failed", "bogus", undefined, null]) {
    for (const esMs of values) {
      for (const roundTripMs of values) {
        for (const total of values) {
          const v = view({ state, esMs, roundTripMs, total });
          const text = [v.es, v.roundTrip, v.count, v.action].flatMap((item) => [item.text, item.name]).join("|");
          assert.doesNotMatch(text, /NaN|undefined|null|Infinity|\[object|-0/, `${state} ${esMs} ${roundTripMs} ${total}: ${text}`);
        }
      }
    }
  }
});

test("an unknown state, or no input at all, is the state before any search", () => {
  assert.deepStrictEqual(view({ state: "bogus", esMs: 1, roundTripMs: 1, total: 1 }), view({ state: "none" }));
  assert.deepStrictEqual(view(), view({ state: "none" }));
  assert.deepStrictEqual(view({}), view({ state: "none" }));
});

test("the control reads the same in every state, and its name starts with the words it shows", () => {
  const states = ["none", "loading", "done", "failed"].map((state) => view({ state, esMs: 1, roundTripMs: 1, total: 1 }).action);
  for (const action of states) {
    assert.equal(action.text, "View query");
    assert.ok(action.name.startsWith(action.text), action.name);
    assert.match(action.name, /Elasticsearch query/);
  }
  assert.ok(states.every((action) => action.name === states[0].name));
});

test("the view is a fresh object each time, so one caller cannot change another's", () => {
  const a = view({ state: "none" });
  a.es.text = "changed";
  assert.equal(view({ state: "none" }).es.text, BLANK);
});
