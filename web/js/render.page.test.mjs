import test from "node:test";
import assert from "node:assert/strict";
import {
  skeletonCount,
  imageIsEager,
  endOfResultsLine,
  emptyStateMessage,
  cardButtonLabel,
} from "./render.js";
import { defaults } from "./state.js";

const n = (value) => value.toLocaleString();

// skeletonCount: how many placeholder cards a loading first page shows

test("the skeleton has one placeholder per card the page asks for", () => {
  assert.equal(skeletonCount(24), 24);
  assert.equal(skeletonCount(1), 1);
  assert.equal(skeletonCount(100), 100);
});

test("a page size that is not a positive whole number draws no skeleton", () => {
  for (const size of [0, -1, 2.5, Number.NaN, undefined, null, "24"]) {
    assert.equal(skeletonCount(size), 0, String(size));
  }
});

// imageIsEager: which card art loads before the rest

test("the first six images of the first page load eagerly", () => {
  for (let index = 0; index < 6; index++) assert.equal(imageIsEager(index, 1), true, String(index));
});

test("the rest of the first page and every appended page stay lazy", () => {
  for (let index = 6; index < 24; index++) assert.equal(imageIsEager(index, 1), false, String(index));
  for (const page of [2, 3, 400]) {
    for (let index = 0; index < 24; index++) assert.equal(imageIsEager(index, page), false, `${page} ${index}`);
  }
});

// endOfResultsLine: the line under the last reachable page

test("the last reachable page of a larger result says how far the page can go", () => {
  assert.equal(
    endOfResultsLine({ shown: 9600, total: 20635, window: 9600 }),
    `Showing the first ${n(9600)} of ${n(20635)} cards. Narrow the search or add filters to see the rest.`,
  );
  assert.equal(
    endOfResultsLine({ shown: 9600, total: 9601, window: 9600 }),
    `Showing the first ${n(9600)} of ${n(9601)} cards. Narrow the search or add filters to see the rest.`,
  );
});

test("no line before the last reachable page", () => {
  assert.equal(endOfResultsLine({ shown: 24, total: 20635, window: 9600 }), "");
  assert.equal(endOfResultsLine({ shown: 9576, total: 20635, window: 9600 }), "");
});

test("no line when the last page is the end of the results", () => {
  assert.equal(endOfResultsLine({ shown: 9600, total: 9600, window: 9600 }), "");
  assert.equal(endOfResultsLine({ shown: 5000, total: 5000, window: 5016 }), "");
  assert.equal(endOfResultsLine({ shown: 0, total: 0, window: 0 }), "");
});

// emptyStateMessage: the sentence on a search that found nothing

const withState = (fields) => ({ ...defaults, types: [], ...fields });

test("a query with no filters names the query and does not mention filters", () => {
  const message = emptyStateMessage(withState({ q: "zzzzqqqq" }));
  assert.equal(message, 'No cards match "zzzzqqqq".');
  assert.doesNotMatch(message, /filter/i);
});

test("the query is named as the search sends it, trimmed", () => {
  assert.equal(emptyStateMessage(withState({ q: "  zzzzqqqq  " })), 'No cards match "zzzzqqqq".');
});

test("a very long query is cut short in the message", () => {
  const q = "a".repeat(200);
  const message = emptyStateMessage(withState({ q }));
  assert.ok(message.length < 100, message);
  assert.match(message, /…"\.$/);
});

test("a query with filters says the filters may be excluding results", () => {
  for (const fields of [{ supertype: "trainer" }, { types: ["Fire"] }, { set: "base1" }, { rarity: "Rare" }, { series: "Base" }]) {
    const message = emptyStateMessage(withState({ q: "zzzzqqqq", ...fields }));
    assert.equal(message, 'No cards match "zzzzqqqq" with these filters, which may be excluding results.', JSON.stringify(fields));
  }
});

test("filters with no query say the filters may be excluding results", () => {
  assert.equal(
    emptyStateMessage(withState({ rarity: "Rare" })),
    "No cards match these filters, which may be excluding results.",
  );
});

test("a browse with no query and no filters gets a plain line", () => {
  assert.equal(emptyStateMessage(withState({})), "No cards to show.");
  assert.equal(emptyStateMessage(withState({ q: "   " })), "No cards to show.");
});

// cardButtonLabel: the card button's accessible name starts with what the
// button can show (the name, when the art is missing) and holds the badges'
// words in their order

test("a card with no matched branches is named for the card and its set", () => {
  assert.equal(cardButtonLabel({ name: "Pikachu", set_name: "Base" }, []), "Pikachu, from Base");
});

test("a card's matched branches follow its name in the order the badges show them", () => {
  assert.equal(
    cardButtonLabel({ name: "Pikachu", set_name: "Base" }, ["exact", "fuzzy-name"]),
    "Pikachu exact fuzzy match, from Base",
  );
  assert.equal(cardButtonLabel({ name: "Pidgey", set_name: "Jungle" }, ["text"]), "Pidgey text match, from Jungle");
});

test("the visible text of a card button is a run of words inside its name", () => {
  const words = (text) => text.toLowerCase().replace(/[^\p{L}\p{N}\s]/gu, " ").split(/\s+/).filter(Boolean).join(" ");
  const card = { name: "Mr. Mime", set_name: "Jungle" };
  const label = words(cardButtonLabel(card, ["prefix", "text"]));
  assert.ok(label.startsWith(words("Mr. Mime prefix text")), label);
  assert.ok(label.includes(words("prefix text")), label);
});
