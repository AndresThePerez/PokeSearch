import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { correctionView } from "./render.js";

// correctionView: whether the server's spelling correction is offered for a
// response, where it goes, what it says and the query it runs.

test("a correction on a search that found nothing goes with the empty-state message", () => {
  const view = correctionView({ suggestion: "eevee", total: 0, q: "eevio" });
  assert.equal(view.shown, true);
  assert.equal(view.place, "empty");
  assert.equal(view.query, "eevee");
  assert.equal(view.text, "Did you mean eevee?");
});

test("a correction that arrives with results goes above them", () => {
  const view = correctionView({ suggestion: "charizard", total: 108, q: "charzard" });
  assert.equal(view.shown, true);
  assert.equal(view.place, "results");
  assert.equal(view.query, "charizard");
  assert.equal(view.text, "Did you mean charizard?");
});

test("a response with no correction offers none", () => {
  for (const suggestion of [undefined, null, "", "   ", 3, {}, ["eevee"]]) {
    for (const total of [0, 12]) {
      assert.equal(correctionView({ suggestion, total, q: "eevio" }).shown, false, `${JSON.stringify(suggestion)} / ${total}`);
    }
  }
});

test("a correction that is the query already searched is not offered", () => {
  assert.equal(correctionView({ suggestion: "eevee", total: 0, q: "eevee" }).shown, false);
  assert.equal(correctionView({ suggestion: "Eevee", total: 0, q: "  eevee " }).shown, false);
});

test("the correction is kept as text, markup and quotes included, and trimmed of spaces", () => {
  const odd = `<b onclick="x()">"pika" & 'chu'</b>`;
  const view = correctionView({ suggestion: ` ${odd} `, total: 0, q: "x" });
  assert.equal(view.query, odd);
  assert.equal(view.text, `Did you mean ${odd}?`);
});

test("a page of results that Load more appends leaves the correction as it is", () => {
  assert.equal(correctionView({ suggestion: "eevee", total: 40, q: "eevio", append: true }), null);
  assert.equal(correctionView({ suggestion: undefined, total: 40, q: "eevio", append: true }), null);
});

test("a total that is not a number of cards puts the correction with the results, where the grid stays", () => {
  assert.equal(correctionView({ suggestion: "eevee", total: undefined, q: "eevio" }).place, "results");
});

// The markup and the code around it, read as text.

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const strip = (source) => source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
const page = read("../index.html");
const render = strip(read("./render.js"));
const main = strip(read("./main.js"));
const styles = read("../styles.css").replace(/\/\*[\s\S]*?\*\//g, "");

test("the correction's accessible name is its visible text: no label overrides it", () => {
  const tag = page.match(/<[^>]*id="did-you-mean"[^>]*>/)?.[0] ?? "";
  assert.ok(tag, "the button is in the markup");
  assert.doesNotMatch(tag, /aria-label/);
  const paint = render.match(/function renderDidYouMean\([^)]*\) \{[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(paint, "renderDidYouMean is in render.js");
  assert.doesNotMatch(paint, /setAttribute\("aria-label"/);
  assert.doesNotMatch(paint, /innerHTML/);
});

test("pressing the correction runs it as a committed search, the way an example search runs", () => {
  const handler = main.match(/\$\("did-you-mean"\)\.addEventListener\("click", [\s\S]*?\n {2}\}\);/)?.[0] ?? "";
  assert.ok(handler, "the click handler is in main.js");
  assert.doesNotMatch(handler, /runSearch\(/, "not a bare search");
  const shared = main.match(/async function (\w+)\(q, pressed\) \{[\s\S]*?commitQuery\("enter"\)[\s\S]*?\n\}/);
  assert.ok(shared, "one function commits a query the way Enter does");
  assert.match(handler, new RegExp(`${shared[1]}\\(`), "the correction goes through it");
});

test("the Stats view hides the correction wherever it sits", () => {
  assert.match(styles, /body\.route-stats #did-you-mean[,\s{]/);
});
