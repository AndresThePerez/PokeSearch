import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  acceptedFilters,
  addressWithFilters,
  rewriteFilterAddress,
  readStateFromURL,
  state,
  ENERGY_TYPES,
} from "./state.js";

// acceptedFilters: the filters a search actually used, read off its answer.

const bare = { q: "", supertype: "", types: [], set: "", rarity: "", series: "", sort: "", order: "", page: 1 };
const from = (fields) => ({ ...bare, ...fields, types: [...(fields.types ?? [])] });

// The rarity facet of a search, as the server sends it: buckets with cards
// under the search's other filters, each with its stored spelling.
const rarityFacet = (...values) => ({ rarity: values.map((value) => ({ value, count: 5 })) });

function deepFreeze(value) {
  if (value && typeof value === "object") {
    for (const key of Object.keys(value)) deepFreeze(value[key]);
    Object.freeze(value);
  }
  return value;
}

test("a types member the server dropped gets no chip, and the address must change", () => {
  const result = acceptedFilters({ state: from({ types: ["Fire", "Bogus"] }), total: 159, facets: rarityFacet("Rare Holo") });
  assert.deepStrictEqual(result.types, ["Fire"]);
  assert.equal(result.changed, true);
});

test("Steel and Normal are the server's Metal and Colorless, and a type appears once", () => {
  assert.deepStrictEqual(acceptedFilters({ state: from({ types: ["Steel"] }), total: 981, facets: {} }).types, ["Metal"]);
  assert.deepStrictEqual(acceptedFilters({ state: from({ types: ["Steel", "Metal"] }), total: 981, facets: {} }).types, ["Metal"]);
  assert.deepStrictEqual(acceptedFilters({ state: from({ types: ["normal", "Colorless", "fire", " Water "] }), total: 3, facets: {} }).types, ["Colorless", "Fire", "Water"]);
});

test("types the server takes as they are change nothing", () => {
  const result = acceptedFilters({ state: from({ types: ["Fire", "Metal"] }), total: 0, facets: {} });
  assert.deepStrictEqual(result.types, ["Fire", "Metal"]);
  assert.equal(result.changed, false);
});

test("a types list with no member the server knows becomes no types filter", () => {
  const result = acceptedFilters({ state: from({ types: ["Bogus", "Plasma"] }), total: 20635, facets: {} });
  assert.deepStrictEqual(result.types, []);
  assert.equal(result.changed, true);
});

test("the eleven types are the server's", () => {
  assert.deepStrictEqual([...ENERGY_TYPES].sort(), ["Colorless", "Darkness", "Dragon", "Fairy", "Fighting", "Fire", "Grass", "Lightning", "Metal", "Psychic", "Water"]);
});

test("a rarity in another spelling the server mapped becomes the stored value", () => {
  const result = acceptedFilters({ state: from({ types: ["Fire", "Bogus"], rarity: "rare holo" }), total: 159, facets: rarityFacet("Common", "Rare Holo", "Rare") });
  assert.equal(result.rarity, "Rare Holo");
  assert.equal(result.changed, true);
});

test("the rarity fold is the server's: letter case, runs of spaces and the ends", () => {
  for (const given of ["  RARE   holo ", "Rare\tHolo", "rare holo"]) {
    assert.equal(acceptedFilters({ state: from({ rarity: given }), total: 4, facets: rarityFacet("Rare Holo") }).rarity, "Rare Holo", JSON.stringify(given));
  }
});

test("an ambiguous fold is left as given", () => {
  const facets = rarityFacet("Rare Holo EX", "Rare Holo ex", "Rare Holo");
  const result = acceptedFilters({ state: from({ rarity: "rare holo ex" }), total: 0, facets });
  assert.equal(result.rarity, "rare holo ex");
  assert.equal(result.changed, false);
});

test("a fold that matches one listed value is left as given when the search found nothing", () => {
  // The server folds against all 44 values; the facet lists only those with
  // cards under the other filters. A search that found nothing did not use
  // the listed value, or its count would be the total.
  const result = acceptedFilters({ state: from({ rarity: "rare holo ex" }), total: 0, facets: rarityFacet("Rare Holo EX") });
  assert.equal(result.rarity, "rare holo ex");
  assert.equal(result.changed, false);
});

test("each half of the pair that shares a fold, given exactly, stays", () => {
  for (const exact of ["Rare Holo EX", "Rare Holo ex"]) {
    const result = acceptedFilters({ state: from({ rarity: exact }), total: 1, facets: rarityFacet("Rare Holo EX", "Rare Holo ex") });
    assert.equal(result.rarity, exact);
    assert.equal(result.changed, false);
  }
});

test("a rarity, series or set that matches no value is still what the search used: it stays", () => {
  const result = acceptedFilters({
    state: from({ rarity: "Bogus", series: "Nope", set: "zz9" }),
    total: 0,
    facets: { ...rarityFacet("Common", "Rare"), set_series: [{ value: "Base", count: 3 }], sets: [{ value: "base1", label: "Base", count: 0 }] },
  });
  assert.equal(result.rarity, "Bogus");
  assert.equal(result.series, "Nope");
  assert.equal(result.set, "zz9");
  assert.equal(result.changed, false);
});

test("series and set are not folded: the server matches them exactly", () => {
  const result = acceptedFilters({
    state: from({ series: "base", set: "BASE1" }),
    total: 0,
    facets: { set_series: [{ value: "Base", count: 3 }], sets: [{ value: "base1", label: "Base", count: 0 }] },
  });
  assert.equal(result.series, "base");
  assert.equal(result.set, "BASE1");
});

test("a value that is only spaces was no filter to the server, and spaces around a set or series are trimmed as the server trims them", () => {
  const result = acceptedFilters({ state: from({ rarity: "   ", series: " ", set: " base1 " }), total: 20635, facets: {} });
  assert.equal(result.rarity, "");
  assert.equal(result.series, "");
  assert.equal(result.set, "base1");
  assert.equal(result.changed, true);
});

test("a label is never taken for the value it names", () => {
  const result = acceptedFilters({ state: from({ rarity: "Mega Attack Rare" }), total: 0, facets: rarityFacet("MEGA_ATTACK_RARE") });
  assert.equal(result.rarity, "Mega Attack Rare");
});

test("an answer without facets, or with facets of the wrong shape, changes no rarity", () => {
  for (const facets of [undefined, null, {}, { rarity: null }, { rarity: "Rare Holo" }, { rarity: [null, 3, { value: 7 }] }]) {
    const result = acceptedFilters({ state: from({ rarity: "rare holo", types: ["Fire"] }), total: 3, facets });
    assert.equal(result.rarity, "rare holo", JSON.stringify(facets));
    assert.equal(result.changed, false);
  }
});

test("the inputs are left as they were", () => {
  const input = deepFreeze(from({ types: ["Steel", "Bogus"], rarity: "rare holo", set: " base1 " }));
  const facets = deepFreeze(rarityFacet("Rare Holo"));
  const result = acceptedFilters({ state: input, total: 3, facets });
  assert.deepStrictEqual(input.types, ["Steel", "Bogus"]);
  assert.ok(result.types !== input.types, "a new list");
  assert.deepStrictEqual(result.types, ["Metal"]);
});

// addressWithFilters: the address with only the filter fields rewritten.

test("the address keeps every other field, its order and the query as the address holds it", () => {
  assert.equal(
    addressWithFilters("?q=pika&types=Fire%2CBogus&rarity=rare+holo&sort=hp", { types: ["Fire"], rarity: "Rare Holo", series: "", set: "" }),
    "q=pika&types=Fire&rarity=Rare+Holo&sort=hp",
  );
});

test("a filter that is now empty leaves the address, and a list is written with commas", () => {
  assert.equal(addressWithFilters("?types=Bogus&q=x", { types: [], rarity: "", series: "", set: "" }), "q=x");
  assert.equal(addressWithFilters("?types=steel,normal", { types: ["Metal", "Colorless"], rarity: "", series: "", set: "" }), "types=Metal%2CColorless");
  assert.equal(addressWithFilters("", { types: [], rarity: "", series: "", set: "" }), "");
});

// rewriteFilterAddress, with plain stand-ins for location and history.

function stage(search, hash, entryState) {
  const calls = [];
  globalThis.location = { search, pathname: "/", hash };
  globalThis.history = {
    state: entryState,
    pushState(s, _title, url) { calls.push({ op: "push", state: s, url }); },
    replaceState(s, _title, url) { calls.push({ op: "replace", state: s, url }); },
  };
  readStateFromURL();
  return calls;
}

test("the rewrite replaces the entry, keeps its state and hash, and never pushes", () => {
  const marker = { cardOverlay: true };
  const calls = stage("?types=Fire,Bogus&rarity=rare+holo", "#card=base1-4", marker);
  state.types = ["Fire"];
  state.rarity = "Rare Holo";
  rewriteFilterAddress();
  assert.equal(calls.length, 1);
  assert.equal(calls[0].op, "replace");
  assert.equal(calls[0].url, "/?types=Fire&rarity=Rare+Holo#card=base1-4");
  assert.ok(calls[0].state === marker, "the entry keeps its state");
});

test("text typed after the search started is not written by the rewrite", () => {
  const calls = stage("?q=pika&types=Bogus", "", null);
  state.q = "pikachu";
  state.types = [];
  rewriteFilterAddress();
  assert.deepStrictEqual(calls.map(({ op, url }) => `${op} ${url}`), ["replace /?q=pika"]);
});

test("an address that already holds the filters is not written", () => {
  const calls = stage("?types=Fire", "", null);
  rewriteFilterAddress();
  assert.equal(calls.length, 0);
});

// Where the page applies it.

test("a first page of results aligns the state before the controls and chips are painted", () => {
  const render = readFileSync(new URL("./render.js", import.meta.url), "utf8").replace(/^\s*\/\/.*$/gm, "");
  const body = render.match(/export function renderResults\([^)]*\) \{[\s\S]*?\n\}/)?.[0] ?? "";
  const align = body.indexOf("acceptedFilters(");
  assert.ok(align !== -1, "renderResults asks acceptedFilters");
  assert.ok(align < body.indexOf("renderFacets("), "before the facets and chips are painted");
  assert.match(body.slice(0, align), /if \(!append\)/, "only for a first page");
  assert.match(body, /rewriteFilterAddress\(\)/);
  globalThis.location = { search: "", pathname: "/", hash: "" };
  readStateFromURL();
});
