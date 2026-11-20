import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { correctionView } from "./render.js";

// The spelling correction's view, pushed with inputs a response or a
// hand-edited address can produce.

test("whatever the suggestion, its text reads Did you mean, then the suggestion trimmed, then a question mark", () => {
  const suggestions = [
    "eevee", "  eevee\t", " eevee ", "\neevee\n", "Flabébé", "pokémon", "😀 eevee", "‮eevee‬", "a​b",
    "x".repeat(300), "<b>\"x\" & 'y'</b>", "a  b   c", "100%", "?", "Did you mean eevee?", "　eevee　",
  ];
  for (const suggestion of suggestions) {
    for (const total of [0, -0, 1, 24, 9600]) {
      const view = correctionView({ suggestion, total, q: "zzzz" });
      assert.equal(view.shown, true, JSON.stringify(suggestion));
      assert.equal(view.query, suggestion.trim());
      assert.equal(view.text, `Did you mean ${suggestion.trim()}?`);
      assert.ok(view.query.length > 0);
    }
  }
});

test("a correction is placed with the message only on a total of zero, minus zero included, and with the results on any other", () => {
  assert.equal(correctionView({ suggestion: "a", total: 0, q: "b" }).place, "empty");
  assert.equal(correctionView({ suggestion: "a", total: -0, q: "b" }).place, "empty");
  for (const total of [1, 2, 24, 9600, 1e9]) assert.equal(correctionView({ suggestion: "a", total, q: "b" }).place, "results");
});

test("a correction that differs from the query only by an accent is still offered, and one that differs only by letter case or edge spaces is not", () => {
  assert.equal(correctionView({ suggestion: "Flabébé", total: 0, q: "flabebe" }).shown, true);
  assert.equal(correctionView({ suggestion: "flabebe", total: 0, q: "Flabébé" }).shown, true);
  for (const q of ["EEVEE", "eevee", "  Eevee  ", "\teevee\n", " eevee"]) {
    assert.equal(correctionView({ suggestion: "eevee", total: 0, q }).shown, false, JSON.stringify(q));
  }
  assert.equal(correctionView({ suggestion: "  EEVEE ", total: 5, q: "eevee" }).shown, false);
});

test("an appended page never changes the correction, whatever the response or the query held", () => {
  for (const append of [true, 1, "yes", {}, []]) {
    for (const suggestion of ["eevee", undefined, null, "", 5]) {
      assert.equal(correctionView({ suggestion, total: 24, q: "x", append }), null, `append ${JSON.stringify(append)}`);
    }
  }
  for (const append of [false, 0, "", null, undefined]) {
    assert.notEqual(correctionView({ suggestion: "eevee", total: 24, q: "x", append }), null, `append ${JSON.stringify(append)}`);
  }
});

test("a query of an odd kind never throws and never hides a correction that differs from it", () => {
  for (const q of [undefined, null, 5, NaN, true, {}, [], ["a"], "", " "]) {
    const view = correctionView({ suggestion: "eevee", total: 0, q });
    assert.equal(view.shown, true, JSON.stringify(q));
  }
});

test("a shown view carries exactly the four fields the page reads, and a hidden one only says it is hidden", () => {
  assert.deepStrictEqual(Object.keys(correctionView({ suggestion: "a", total: 0, q: "b" })).sort(), ["place", "query", "shown", "text"]);
  assert.deepStrictEqual(correctionView({ suggestion: "", total: 0, q: "b" }), { shown: false });
});

test("the view is the same for the same response, so painting it twice cannot move the button", () => {
  const first = correctionView({ suggestion: "eevee", total: 3, q: "eevio" });
  assert.deepStrictEqual(correctionView({ suggestion: "eevee", total: 3, q: "eevio" }), first);
});

// The markup the button lives in.

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const page = read("../index.html");
const styles = read("../styles.css").replace(/\/\*[\s\S]*?\*\//g, "");

test("the page holds one correction button, starting hidden inside the empty state, after its message", () => {
  assert.equal((page.match(/id="did-you-mean"/g) ?? []).length, 1);
  const empty = page.match(/<div id="empty-state"[^>]*>([\s\S]*?)<\/div>/)?.[1] ?? "";
  assert.match(empty, /id="empty-message"[\s\S]*id="did-you-mean"[^>]*\bhidden\b/);
});

test("no rule gives the correction button a display that would show it while it is hidden", () => {
  for (const match of styles.matchAll(/([^{}]*)\{([^{}]*)\}/g)) {
    const selectors = match[1].split(",").map((part) => part.trim());
    if (!selectors.some((selector) => selector.endsWith("#did-you-mean"))) continue;
    if (/body\.route-stats/.test(match[1])) continue;
    assert.doesNotMatch(match[2], /display\s*:[^;]*!important/, match[1]);
  }
  assert.match(styles, /\[hidden\]\s*\{\s*display:\s*none\s*!important/);
});
