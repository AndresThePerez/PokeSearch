import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { enterFocusTarget, blurCloses, noteSearchBoxInput } from "./suggest.js";

// The page's markup with its comments taken out, for the ids a focus move may name.
const page = readFileSync(new URL("../index.html", import.meta.url), "utf8").replace(/<!--[\s\S]*?-->/g, "");
const main = readFileSync(new URL("./main.js", import.meta.url), "utf8");

function tagWithID(id) {
  return page.match(new RegExp(`<[a-z0-9]+\\b[^>]*\\bid="${id}"[^>]*>`))?.[0] ?? null;
}

const TARGET = { results: "results-grid", none: "empty-message" };
const OUTCOMES = ["results", "none", "refused", "failed", "aborted", "Results", "", "unknown", undefined, null];

// enterFocusTarget with the two fields that say the move is no longer wanted

test("focus moves only for a still-wanted move: the box has focus, nothing typed since, no later Enter", () => {
  for (const outcome of OUTCOMES) {
    for (const inBox of [false, true]) {
      for (const typedSinceEnter of [false, true]) {
        for (const superseded of [false, true]) {
          const want = inBox && !typedSinceEnter && !superseded ? (TARGET[outcome] ?? null) : null;
          const got = enterFocusTarget({ outcome, inBox, typedSinceEnter, superseded });
          assert.equal(got, want, JSON.stringify({ outcome, inBox, typedSinceEnter, superseded }));
        }
      }
    }
  }
});

test("either new field alone, or both, cancels a move that would otherwise go to the grid or the message", () => {
  for (const outcome of ["results", "none"]) {
    assert.notEqual(enterFocusTarget({ outcome, inBox: true }), null, "the baseline moves");
    assert.equal(enterFocusTarget({ outcome, inBox: true, typedSinceEnter: true }), null);
    assert.equal(enterFocusTarget({ outcome, inBox: true, superseded: true }), null);
    assert.equal(enterFocusTarget({ outcome, inBox: true, typedSinceEnter: true, superseded: true }), null);
  }
});

test("a call that names neither new field decides as it did before they existed", () => {
  assert.equal(enterFocusTarget({ outcome: "results", inBox: true }), "results-grid");
  assert.equal(enterFocusTarget({ outcome: "none", inBox: true }), "empty-message");
  assert.equal(enterFocusTarget({ outcome: "results", inBox: true, typedSinceEnter: undefined, superseded: undefined }), "results-grid");
  assert.equal(enterFocusTarget({ outcome: "failed", inBox: true }), null);
});

test("non-boolean values read by their truth for the new fields, and a box not known to have focus never moves", () => {
  for (const falsy of [0, "", null, undefined, false]) {
    assert.equal(enterFocusTarget({ outcome: "results", inBox: true, typedSinceEnter: falsy, superseded: falsy }), "results-grid", String(falsy));
  }
  for (const truthy of [1, "yes", {}, [], true]) {
    assert.equal(enterFocusTarget({ outcome: "results", inBox: true, typedSinceEnter: truthy }), null, JSON.stringify(truthy));
    assert.equal(enterFocusTarget({ outcome: "none", inBox: true, superseded: truthy }), null, JSON.stringify(truthy));
  }
  for (const inBox of [0, "", null, undefined, false]) {
    assert.equal(enterFocusTarget({ outcome: "results", inBox, typedSinceEnter: false, superseded: false }), null, String(inBox));
  }
});

test("whatever the inputs, the answer is nothing or the id of a script-focusable element in the page", () => {
  const flags = [true, false, 1, 0, "", "x", null, undefined];
  for (const outcome of OUTCOMES) {
    for (const inBox of flags) {
      for (const typedSinceEnter of flags) {
        for (const superseded of flags) {
          const target = enterFocusTarget({ outcome, inBox, typedSinceEnter, superseded });
          if (target === null) continue;
          const tag = tagWithID(target);
          assert.ok(tag, `no element has the id ${target}`);
          assert.equal(tag.match(/\stabindex="([^"]*)"/)?.[1], "-1", target);
        }
      }
    }
  }
});

// blurCloses

test("the list's delayed close after a blur runs only when the box has not got focus back", () => {
  assert.equal(blurCloses({ focused: false }), true);
  assert.equal(blurCloses({ focused: true }), false);
  for (const focused of [undefined, null, 0, ""]) assert.equal(blurCloses({ focused }), true, String(focused));
  for (const focused of [1, "yes", {}]) assert.equal(blurCloses({ focused }), false, JSON.stringify(focused));
  assert.equal(typeof blurCloses({ focused: true }), "boolean");
  assert.equal(typeof blurCloses({ focused: undefined }), "boolean");
});

// the input counter

test("noting a box input is safe to do over and over and never changes what a move decides", () => {
  for (let i = 0; i < 100000; i += 1) noteSearchBoxInput();
  assert.equal(noteSearchBoxInput(), undefined);
  assert.equal(enterFocusTarget({ outcome: "results", inBox: true }), "results-grid", "the counter is not an input of the decision");
});

// the wiring in main.js, which a test cannot import

test("every input event in the search box is noted, by the one input listener", () => {
  const listener = main.match(/\$\("search-input"\)\.addEventListener\("input",[\s\S]*?\n  \}\);/);
  assert.ok(listener, "the input listener is not where this test looks");
  assert.equal([...listener[0].matchAll(/noteSearchBoxInput\(\)/g)].length, 1);
  assert.equal([...main.matchAll(/noteSearchBoxInput\(\)/g)].length, 1, "nothing else counts inputs");
  // Counted before the search is scheduled, so a handler that throws below cannot skip it.
  assert.ok(listener[0].indexOf("noteSearchBoxInput()") < listener[0].indexOf("scheduleSearch()"));
});

test("the blur handler asks blurCloses with the box's own focus before it closes the list", () => {
  const handler = main.match(/\$\("search-input"\)\.addEventListener\("blur",[\s\S]*?\n  \}\);/);
  assert.ok(handler, "the blur listener is not where this test looks");
  assert.match(handler[0], /commitQuery\("blur"\)/);
  assert.match(handler[0], /blurCloses\(\{\s*focused:\s*document\.activeElement === \$\("search-input"\)\s*\}\)/);
  assert.ok(handler[0].indexOf("blurCloses") < handler[0].indexOf("closeSuggestions()"), "closed only when blurCloses allows");
  assert.match(handler[0], /setTimeout\(/);
});
