import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { queryPanelAction, queryPanelRequest } from "./api.js";

// What opening the query panel asks for, and the rules the request for the
// query alone follows, read from the source where there is no page to run it.

const ANSWERS = ["none", "query-only", "search"];
const VALUES = [true, false, undefined, null, 0, 1, "", "true", "yes", NaN, {}, []];

test("every combination of the three inputs gives exactly one of the three answers, and only true counts as true", () => {
  for (const open of VALUES) {
    for (const hadQuery of VALUES) {
      for (const searching of VALUES) {
        const got = queryPanelAction({ open, hadQuery, searching });
        assert.ok(ANSWERS.includes(got), `${String(open)} ${String(hadQuery)} ${String(searching)}: ${got}`);
        const want = open !== true || hadQuery === true ? "none" : searching === true ? "search" : "query-only";
        assert.equal(got, want, `${String(open)} ${String(hadQuery)} ${String(searching)}`);
      }
    }
  }
});

test("it never searches when a query is already shown, never asks for the query alone while a search runs, and never answers a closed panel", () => {
  for (const open of VALUES) {
    for (const hadQuery of VALUES) {
      for (const searching of VALUES) {
        const got = queryPanelAction({ open, hadQuery, searching });
        if (hadQuery === true) assert.equal(got, "none");
        if (searching === true) assert.notEqual(got, "query-only");
        if (open !== true) assert.equal(got, "none");
      }
    }
  }
});

test("missing input is no request, extra properties are ignored and the input is left as it was", () => {
  assert.equal(queryPanelAction(), "none");
  assert.equal(queryPanelAction({}), "none");
  assert.equal(queryPanelAction({ hadQuery: false, searching: true }), "none");
  const input = Object.freeze({ open: true, hadQuery: false, searching: false, other: "x" });
  assert.equal(queryPanelAction(input), "query-only");
  assert.deepStrictEqual(input, { open: true, hadQuery: false, searching: false, other: "x" });
});

test("with no search running, the request the page makes is the query alone, and closing or a shown query asks for nothing", () => {
  assert.equal(queryPanelRequest(true, false), "query-only");
  assert.equal(queryPanelRequest(true, true), "none");
  assert.equal(queryPanelRequest(false, false), "none");
  assert.equal(queryPanelRequest(false, true), "none");
  assert.equal(queryPanelRequest(), "none");
  for (let i = 0; i < 5; i++) assert.equal(queryPanelRequest(true, false), "query-only", "asking leaves no search counted as running");
});

const source = readFileSync(new URL("./api.js", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
const body = (name, opening = "{") => {
  const start = source.indexOf(name);
  assert.ok(start !== -1, `no ${name}`);
  const open = source.indexOf(opening, start) + opening.length - 1;
  let depth = 0;
  let at = open;
  for (; at < source.length; at++) {
    if (source[at] === "{") depth++;
    if (source[at] === "}" && --depth === 0) break;
  }
  return source.slice(open + 1, at);
};

test("the request for the query alone paints the rail's two panes and touches nothing else", () => {
  const fn = body("export async function refreshQuery()");
  for (const call of ["runSearch", "renderResults", "setDegraded", "setStale", "setLoading", "showNotice", "hideNotice", "recordTiming", "writeHistory", "writeStateToURL", "history.", "searchController", "scheduleSearch"]) {
    assert.ok(!fn.includes(call), `refreshQuery uses ${call}`);
  }
  assert.doesNotMatch(fn, /state\.\w+\s*=[^=]/, "it writes the page's state");
  assert.equal([...fn.matchAll(/renderInspector\(/g)].length, 1);
  assert.match(fn, /debug:\s*true/);
  assert.match(fn, /\$\("dsl-json"\)\.textContent\s*=\s*QUERY_UNAVAILABLE/);
  assert.doesNotMatch(fn, /\$\("(?!dsl-json")[a-z-]+"\)/, "it reaches for an element other than the query pane");
});

test("both of its paints wait for the serial check, so a search that began after it is never written over", () => {
  const fn = body("export async function refreshQuery()");
  const serialAt = fn.indexOf("const serial = searchSerial");
  assert.ok(serialAt !== -1 && serialAt < fn.indexOf("fetch("), "the serial is taken before the request goes out");
  for (const paint of ["renderInspector(", '$("dsl-json").textContent = QUERY_UNAVAILABLE']) {
    const at = fn.indexOf(paint);
    assert.ok(at !== -1, paint);
    assert.match(fn.slice(Math.max(0, at - 40), at), /serial === searchSerial\)\s*$/, `${paint} is not behind the check`);
  }
});

test("a search takes the serial before its first await and counts itself running until it ends", () => {
  const fn = body("export async function runSearch(", "} = {}) {");
  const first = fn.indexOf("await ");
  assert.ok(first !== -1);
  assert.ok(fn.indexOf("searchSerial += 1") !== -1 && fn.indexOf("searchSerial += 1") < first, "the serial moves after the first await");
  assert.equal([...fn.matchAll(/searchesRunning \+= 1/g)].length, 1);
  assert.equal([...fn.matchAll(/searchesRunning -= 1/g)].length, 1);
  const fin = fn.slice(fn.indexOf("finally"));
  assert.match(fin, /searchesRunning -= 1/, "the count is lowered in finally, so no exit leaves it raised");
  assert.ok(fn.indexOf("searchesRunning += 1") < first);
});

test("the panel's toggle gives the stats route its own request and sends a running search down the old path", () => {
  const main = readFileSync(new URL("./main.js", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  const handler = main.match(/\$\("query-inspector"\)\.addEventListener\("toggle", \(\) => \{([\s\S]*?)\n  \}\);/)?.[1] ?? "";
  assert.ok(handler, "no toggle handler");
  assert.match(handler, /queryPanelRequest\(\$\("query-inspector"\)\.open, lastResponseHadDSL\(\)\)/);
  assert.ok(handler.indexOf('action === "none"') < handler.indexOf("statsViewOpen()"), "the stats route is asked only for an open panel without a query");
  assert.ok(handler.indexOf("statsViewOpen()") < handler.indexOf("refreshQuery()"), "stats comes before the query-only request");
  assert.match(handler, /else if \(action === "query-only"\) refreshQuery\(\);\s*\n\s*else runSearch\(\);/);
});
