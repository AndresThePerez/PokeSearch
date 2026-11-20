import test from "node:test";
import assert from "node:assert/strict";
import { EXAMPLE_QUERIES, examplePlacement, badgeLegend, badgeTitle, branchLabel } from "./render.js";
import { BRANCHES } from "./telemetry.js";
import { defaults } from "./state.js";

const withState = (fields) => ({ ...defaults, types: [], ...fields });

// examplePlacement: every state the page can hold, against every total a
// response (or its absence) can carry.

const TOTALS = [undefined, null, 0, 1, 20635, Number.NaN, "0", "12", ""];

const NO_QUERY_NO_FILTER = [
  {},
  { q: "" },
  { q: "   " },
  { q: "\t\n" },
  { sort: "hp" },
  { sort: "hp", order: "desc" },
  { page: 2 },
  { page: 40, sort: "name" },
  { types: [] },
  { supertype: "", set: "", rarity: "", series: "" },
];

const QUERY_OR_FILTER = [
  { q: "charizard" },
  { q: "  pika  " },
  { q: "x" },
  { types: ["Fire"] },
  { types: ["Fire", "Water"] },
  { supertype: "trainer" },
  { set: "base1" },
  { rarity: "Rare" },
  { series: "Base" },
  { q: "pika", page: 3, sort: "hp" },
  { set: "base1", page: 2 },
];

test("the examples go in exactly one of three places, whatever the state and the total", () => {
  for (const fields of [...NO_QUERY_NO_FILTER, ...QUERY_OR_FILTER]) {
    for (const total of TOTALS) {
      const placement = examplePlacement({ state: withState(fields), total });
      assert.ok(["landing", "empty", "none"].includes(placement), `${JSON.stringify(fields)} total=${String(total)}: ${placement}`);
    }
  }
});

test("the empty state's examples follow a total of zero, and only zero", () => {
  for (const fields of [...NO_QUERY_NO_FILTER, ...QUERY_OR_FILTER]) {
    for (const total of TOTALS) {
      const placement = examplePlacement({ state: withState(fields), total });
      assert.equal(placement === "empty", total === 0, `${JSON.stringify(fields)} total=${String(total)}: ${placement}`);
    }
  }
});

test("the landing is never shown with a query or a filter, and always without, unless nothing was found", () => {
  for (const fields of QUERY_OR_FILTER) {
    for (const total of TOTALS.filter((t) => t !== 0)) {
      assert.equal(examplePlacement({ state: withState(fields), total }), "none", `${JSON.stringify(fields)} total=${String(total)}`);
    }
  }
  for (const fields of NO_QUERY_NO_FILTER) {
    for (const total of TOTALS.filter((t) => t !== 0)) {
      assert.equal(examplePlacement({ state: withState(fields), total }), "landing", `${JSON.stringify(fields)} total=${String(total)}`);
    }
  }
});

// EXAMPLE_QUERIES: data in one place, which nothing can change, and which the
// server accepts.

test("no example can be edited, added or removed at run time", () => {
  assert.throws(() => { EXAMPLE_QUERIES[0].q = "mewtwo"; }, TypeError);
  assert.throws(() => { EXAMPLE_QUERIES[1].shows = "anything"; }, TypeError);
  assert.throws(() => { EXAMPLE_QUERIES.push({ q: "mew", shows: "x" }); }, TypeError);
  assert.throws(() => { EXAMPLE_QUERIES.length = 0; }, TypeError);
  assert.equal(EXAMPLE_QUERIES.length, 5);
  assert.equal(EXAMPLE_QUERIES[0].q, "charizard");
});

test("each example is a query the server accepts and is not a repeat of another", () => {
  const seen = new Set();
  for (const { q, shows } of EXAMPLE_QUERIES) {
    assert.equal(typeof q, "string");
    assert.equal(q, q.trim(), "no surrounding spaces to trim away");
    assert.ok(q.length > 0 && q.length < 256, q);
    assert.ok(q.split(/\s+/).length <= 32, q);
    assert.ok(typeof shows === "string" && shows.length > 0, q);
    assert.ok(!seen.has(q.toLowerCase()), `${q} appears twice`);
    seen.add(q.toLowerCase());
  }
});

// badgeLegend and badgeTitle: one table, read the same way everywhere.

test("each known branch's legend words are the badge table's words and its tooltip's", () => {
  const legend = badgeLegend([["text"]]);
  assert.deepStrictEqual(legend.map((entry) => entry.name), Object.keys(BRANCHES));
  for (const entry of legend) {
    assert.equal(entry.label, BRANCHES[entry.name].label, entry.name);
    assert.equal(entry.meaning, BRANCHES[entry.name].meaning, entry.name);
    assert.equal(badgeTitle(entry.name), `${BRANCHES[entry.name].label}: ${BRANCHES[entry.name].meaning}`);
  }
});

test("a branch repeated within and across cards is listed once, unknown ones in the order they first appear", () => {
  const legend = badgeLegend([["zeta", "exact", "zeta"], ["alpha", "text", "alpha"], ["zeta"], ["exact", "exact"]]);
  assert.deepStrictEqual(legend.map((entry) => entry.name), ["exact", "prefix", "fuzzy-name", "text", "zeta", "alpha"]);
  assert.deepStrictEqual(
    badgeLegend([["text"], ["prefix"]]).map((entry) => entry.name),
    badgeLegend([["prefix"], ["exact", "text"]]).map((entry) => entry.name),
    "the known part of the line reads the same on every search",
  );
});

// A branch name arrives from the server as a plain string. Names that happen
// to be keys of every JavaScript object are still names the page does not
// know, and show up under their own name like any other.
test("a branch named like a built-in object key shows under its own name, in the legend and the tooltip", () => {
  for (const name of ["constructor", "__proto__", "toString", "hasOwnProperty", "valueOf"]) {
    assert.equal(branchLabel(name), name, `badge word for ${name}`);
    assert.ok(badgeTitle(name).startsWith(`${name}: `), `tooltip for ${name}: ${badgeTitle(name).slice(0, 60)}`);
    const entry = badgeLegend([[name]]).at(-1);
    assert.equal(entry.name, name);
    assert.equal(entry.label, name, `legend label for ${name}: ${String(entry.label).slice(0, 60)}`);
  }
});

// matched comes from a response. A shape the server should not send is
// treated as no badges for the cards it cannot read, never as a crash that
// stops the rest of the results from rendering, and never as a badge called
// "null" or "".
test("a matched list of the wrong shape gives a legend without junk, and never throws", () => {
  const shapes = ["exact", { 0: ["exact"] }, 5, true, [null], [null, ["text"]], [["text", null]], [[""]], [[42]], [{}]];
  for (const matched of shapes) {
    let legend;
    assert.doesNotThrow(() => { legend = badgeLegend(matched); }, JSON.stringify(matched));
    for (const entry of legend) {
      assert.ok(typeof entry.name === "string" && entry.name.length > 0, `${JSON.stringify(matched)}: name ${String(entry.name)}`);
      assert.doesNotMatch(`${entry.label} ${entry.meaning}`, /null|undefined|\[object|^\s|  /, JSON.stringify(matched));
    }
  }
});
