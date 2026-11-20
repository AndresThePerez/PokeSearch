import test from "node:test";
import assert from "node:assert/strict";
import {
  skeletonCount,
  imageIsEager,
  endOfResultsLine,
  emptyStateMessage,
  cardButtonLabel,
} from "./render.js";
import { SEARCH_PAGE_SIZE } from "./api.js";
import { defaults } from "./state.js";

const n = (value) => value.toLocaleString();
const withState = (fields) => ({ ...defaults, types: [], ...fields });

// skeletonCount and imageIsEager

test("the skeleton for the page size the request carries is exactly that many cards", () => {
  assert.equal(skeletonCount(SEARCH_PAGE_SIZE), SEARCH_PAGE_SIZE);
});

test("a page size that is not a positive whole number draws no skeleton, however odd", () => {
  for (const size of [0, -0, -24, 0.5, 23.999, Infinity, -Infinity, NaN, "24", "", null, undefined, [], {}, true, 24n]) {
    assert.equal(skeletonCount(size), 0, String(typeof size === "bigint" ? `${size}n` : JSON.stringify(size) ?? size));
  }
});

test("exactly the first six images of the first page are eager, and nothing else is", () => {
  let eager = 0;
  for (let index = 0; index < 24; index += 1) if (imageIsEager(index, 1)) eager += 1;
  assert.equal(eager, 6);
  assert.equal(imageIsEager(5, 1), true);
  assert.equal(imageIsEager(6, 1), false);
  for (const page of [0, 2, 3, 400, "1", 1.5, -1, NaN, undefined, null, true]) {
    for (let index = 0; index < 24; index += 1) {
      assert.equal(imageIsEager(index, page), false, `page ${String(page)} index ${index}`);
    }
  }
  for (const index of [NaN, undefined, 6, 7.5, 1000]) {
    assert.equal(imageIsEager(index, 1), false, `index ${String(index)}`);
  }
});

// endOfResultsLine

test("the line appears on the last page of a result larger than the window, and on no other page", () => {
  const size = 24;
  for (const total of [1, 24, 25, 5000, 9599, 9600, 9601, 9624, 20635]) {
    const pages = Math.min(Math.ceil(total / size), 400);
    const window = pages * size;
    for (let page = 1; page <= pages; page += 1) {
      const shown = Math.min(page * size, total);
      const line = endOfResultsLine({ shown, total, window });
      const last = page === pages;
      const wants = last && total > window;
      assert.equal(line !== "", wants, `total ${total} page ${page}`);
      if (wants) {
        assert.ok(line.includes(n(shown)) && line.includes(n(total)), line);
        assert.doesNotMatch(line, /NaN|undefined|null/);
      }
    }
  }
});

test("the line's boundaries: at the window, one past it, and a result that ends inside it", () => {
  assert.equal(endOfResultsLine({ shown: 9600, total: 9600, window: 9600 }), "", "exactly the window is the end");
  assert.notEqual(endOfResultsLine({ shown: 9600, total: 9601, window: 9600 }), "", "one card beyond the window");
  assert.equal(endOfResultsLine({ shown: 9599, total: 9601, window: 9600 }), "", "not yet at the window");
  assert.equal(endOfResultsLine({ shown: 9700, total: 9600, window: 9600 }), "", "more shown than exist");
  assert.equal(endOfResultsLine({ shown: 0, total: 5, window: 0 }), "", "no pages reached");
  assert.equal(endOfResultsLine({ shown: 0, total: 0, window: 0 }), "");
  assert.equal(endOfResultsLine({ shown: 10, total: 100, window: -5 }), "");
  assert.equal(endOfResultsLine({ shown: 10, total: 100, window: Number.NaN }), "");
});

// emptyStateMessage

test("a query of 40, 41 and 400 characters is cut at 40 with one ellipsis, and only when longer", () => {
  assert.equal(emptyStateMessage(withState({ q: "a".repeat(40) })), `No cards match "${"a".repeat(40)}".`);
  assert.equal(emptyStateMessage(withState({ q: "a".repeat(41) })), `No cards match "${"a".repeat(40)}…".`);
  assert.equal(emptyStateMessage(withState({ q: "a".repeat(400) })), `No cards match "${"a".repeat(40)}…".`);
  // Spaces around the query do not count towards the 40.
  assert.equal(emptyStateMessage(withState({ q: `  ${"a".repeat(40)}  ` })), `No cards match "${"a".repeat(40)}".`);
});

test("the cut never splits a character: no lone surrogate, whatever sits at the 40th place", () => {
  const pieces = ["😀", "🔥", "𝒜", "é", "é", "🇫🇷", "👨‍👩‍👧", "日本語"];
  for (const piece of pieces) {
    for (const lead of [0, 1, 2, 39, 40]) {
      const q = `${"x".repeat(lead)}${piece.repeat(60)}`;
      const message = emptyStateMessage(withState({ q }));
      assert.ok(message.isWellFormed(), `${piece} after ${lead}: ${JSON.stringify(message)}`);
      assert.ok(Array.from(message).length < 80, message);
    }
  }
});

test("markup and quotes in a query come back as the characters typed, for textContent to show", () => {
  const q = `<b onclick=f()> "q" & 'x'`;
  assert.equal(emptyStateMessage(withState({ q })), `No cards match "${q}".`);
});

test("each filter alone is enough to say the filters may be excluding results, and empty ones are not filters", () => {
  const alone = [{ supertype: "Trainer" }, { types: ["Fire"] }, { set: "base1" }, { rarity: "Rare" }, { series: "Base" }];
  for (const fields of alone) {
    for (const q of ["", "   ", "zzzz"]) {
      const message = emptyStateMessage(withState({ q, ...fields }));
      assert.match(message, /filters/, `${JSON.stringify(fields)} ${JSON.stringify(q)}`);
      assert.match(message, /excluding/, message);
    }
  }
  for (const empty of [{}, { types: [] }, { supertype: "" }, { set: "", rarity: "", series: "" }, { sort: "hp", order: "asc", page: 3 }]) {
    for (const q of ["", "zzzz"]) {
      assert.doesNotMatch(emptyStateMessage(withState({ q, ...empty })), /filter/i, `${JSON.stringify(empty)} ${JSON.stringify(q)}`);
    }
  }
});

test("every state the page can hold gives one non-empty sentence, with no stray words from a missing value", () => {
  const queries = ["", " ", "pika", "a".repeat(300), "😀".repeat(50), undefined, null];
  const filters = [{}, { supertype: "Trainer" }, { types: ["Fire", "Water"] }, { set: "x", rarity: "y", series: "z" }];
  for (const q of queries) {
    for (const f of filters) {
      const given = { ...defaults, ...f, q };
      if (q === undefined) delete given.q;
      const copy = JSON.stringify(given);
      const message = emptyStateMessage(given);
      assert.equal(typeof message, "string");
      assert.ok(message.trim().length > 0, copy);
      assert.doesNotMatch(message, /undefined|null|NaN|\[object/, copy);
      assert.doesNotMatch(message, /  |\s$|^\s/, copy);
      assert.equal(JSON.stringify(given), copy, "the state is not touched");
    }
  }
  assert.doesNotThrow(() => emptyStateMessage({}), "a state with no fields at all");
});

// cardButtonLabel

test("a card button's name always holds the visible name first and reads as one clean sentence", () => {
  const names = ["Pikachu", "Mr. Mime", 'Rocket\'s "Zapdos"', "<b>Bold</b>", "Flabébé", "Nidoran♀", "A"];
  const sets = ["Base", "Gym Heroes", "Sword & Shield"];
  const branchSets = [[], ["exact"], ["exact", "prefix", "fuzzy-name", "text"], ["brand-new-branch"], ["text", "text"]];
  for (const name of names) {
    for (const set_name of sets) {
      for (const branches of branchSets) {
        const label = cardButtonLabel({ name, set_name }, branches);
        assert.ok(label.startsWith(name), label);
        assert.ok(label.endsWith(`, from ${set_name}`), label);
        assert.doesNotMatch(label, /undefined|null|NaN/, label);
        assert.doesNotMatch(label, /\s{2,}|,\s*,|\s,/, label);
      }
    }
  }
});

test("a branch the page has no badge for is named as the server sent it, and the order given is kept", () => {
  assert.equal(
    cardButtonLabel({ name: "Pidgey", set_name: "Jungle" }, ["text", "brand-new-branch", "exact"]),
    "Pidgey text brand-new-branch exact match, from Jungle",
  );
  assert.equal(cardButtonLabel({ name: "Pidgey", set_name: "Jungle" }), "Pidgey, from Jungle", "no branches argument at all");
});
