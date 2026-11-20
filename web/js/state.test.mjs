import test from "node:test";
import assert from "node:assert/strict";
import {
  state,
  defaults,
  readStateFromURL,
  buildParams,
  queryText,
  effectiveSort,
  effectiveOrder,
} from "./state.js";

// readStateFromURL reads the global location at call time. A plain object is
// enough; nothing else of the browser is touched.
function load(search = "") {
  globalThis.location = { search, pathname: "/", hash: "" };
  readStateFromURL();
}

test.beforeEach(() => load(""));
test.after(() => {
  delete globalThis.location;
});

// readStateFromURL

test("readStateFromURL on an empty search gives the defaults", () => {
  assert.deepStrictEqual(state, defaults);
  assert.equal(state.page, 1);
});

test("readStateFromURL reads every filter from the querystring", () => {
  load("?q=pika&supertype=Pok%C3%A9mon&types=Fire,Water&set=base1&rarity=Rare%20Holo&series=Base&sort=hp&order=asc");
  assert.equal(state.q, "pika");
  assert.equal(state.supertype, "Pokémon");
  assert.deepStrictEqual(state.types, ["Fire", "Water"]);
  assert.equal(state.set, "base1");
  assert.equal(state.rarity, "Rare Holo");
  assert.equal(state.series, "Base");
  assert.equal(state.sort, "hp");
  assert.equal(state.order, "asc");
});

test("readStateFromURL decodes an encoded comma in types as a separator", () => {
  load("?types=Fire%2CWater");
  assert.deepStrictEqual(state.types, ["Fire", "Water"]);
});

test("readStateFromURL drops empty items from the types list", () => {
  load("?types=,Fire,,Water,");
  assert.deepStrictEqual(state.types, ["Fire", "Water"]);
  load("?types=");
  assert.deepStrictEqual(state.types, []);
});

test("readStateFromURL ignores unknown parameters and accepts a search without the leading ?", () => {
  load("?foo=bar&q=mew");
  assert.equal(state.q, "mew");
  assert.equal(Object.hasOwn(state, "foo"), false);
  load("q=mew");
  assert.equal(state.q, "mew");
});

test("readStateFromURL does not read page or debug from the address", () => {
  load("?q=mew&page=5&debug=1");
  assert.equal(state.page, 1);
  assert.equal(Object.hasOwn(state, "debug"), false);
});

test("readStateFromURL starts from a clean state each time", () => {
  load("?q=mew&types=Fire&sort=hp");
  state.page = 7;
  load("");
  assert.deepStrictEqual(state, defaults);
  // The defaults object is never the live state, so a write cannot leak into it.
  assert.ok(state !== defaults, "state must be a copy of defaults");
  assert.deepStrictEqual(defaults.types, []);
});

test("readStateFromURL keeps percent-encoded and plus-encoded text readable", () => {
  load("?q=mr.%20mime");
  assert.equal(state.q, "mr. mime");
  load("?q=mr.+mime");
  assert.equal(state.q, "mr. mime");
  load("?q=%C3%A9v%C3%A9e");
  assert.equal(state.q, "évée");
});

test("readStateFromURL keeps a repeated parameter's first value", () => {
  load("?q=first&q=second");
  assert.equal(state.q, "first");
});

// queryText

test("queryText trims the query", () => {
  load("?q=%20%20pika%20");
  assert.equal(queryText(), "pika");
  load("");
  assert.equal(queryText(), "");
});

// buildParams

test("buildParams of an empty state is empty", () => {
  assert.equal(buildParams().toString(), "");
  assert.equal(buildParams({ page: true }).toString(), "");
});

test("buildParams with page and debug on a sample address", () => {
  load("?q=pika&types=Fire,Water&sort=hp");
  assert.equal(
    buildParams({ page: true, debug: true }).toString(),
    "q=pika&types=Fire%2CWater&sort=hp&debug=1",
  );
});

test("buildParams leaves page and debug out unless asked", () => {
  load("?q=pika");
  state.page = 3;
  assert.equal(buildParams().toString(), "q=pika");
  assert.equal(buildParams({ debug: true }).toString(), "q=pika&debug=1");
  assert.equal(buildParams({ page: true }).toString(), "q=pika&page=3");
});

test("buildParams leaves page 1 out even when asked", () => {
  load("?q=pika");
  state.page = 1;
  assert.equal(buildParams({ page: true }).toString(), "q=pika");
});

test("buildParams writes the parameters in a fixed order", () => {
  load("?order=desc&sort=hp&series=Base&rarity=Rare&set=base1&types=Fire&supertype=Trainer&q=ball");
  assert.deepStrictEqual(
    [...buildParams({ page: true, debug: true }).keys()],
    ["q", "supertype", "types", "set", "rarity", "series", "sort", "order", "debug"],
  );
});

test("buildParams trims the query and omits a blank one", () => {
  load("?q=%20pika%20");
  assert.equal(buildParams().get("q"), "pika");
  load("?q=%20%20");
  assert.equal(buildParams().has("q"), false);
});

test("buildParams percent-encodes spaces, accents and reserved characters", () => {
  load("?q=mr.%20mime&rarity=Rare%20Holo&supertype=Pok%C3%A9mon");
  assert.equal(buildParams().toString(), "q=mr.+mime&supertype=Pok%C3%A9mon&rarity=Rare+Holo");
  state.q = "ex10-?&x=1";
  assert.equal(buildParams().toString().split("&")[0], "q=ex10-%3F%26x%3D1");
  assert.equal(buildParams().get("q"), "ex10-?&x=1");
});

test("buildParams round-trips through readStateFromURL", () => {
  load("?q=pika%20chu&supertype=Pok%C3%A9mon&types=Fire,Water&set=base1&rarity=Rare%20Holo&series=Base&sort=name&order=desc");
  const before = { ...state, types: [...state.types] };
  load(`?${buildParams()}`);
  assert.deepStrictEqual(state, before);
});

// effectiveSort and effectiveOrder

test("effectiveSort defaults to relevance with a query and newest without one", () => {
  assert.equal(effectiveSort(), "newest");
  load("?q=pika");
  assert.equal(effectiveSort(), "relevance");
});

test("effectiveSort treats a blank query as no query", () => {
  load("?q=%20%20");
  assert.equal(effectiveSort(), "newest");
});

test("effectiveSort falls back to newest when relevance is asked for with no query", () => {
  load("?sort=relevance");
  assert.equal(effectiveSort(), "newest");
  load("?sort=relevance&q=pika");
  assert.equal(effectiveSort(), "relevance");
});

test("effectiveSort keeps an explicit sort, with or without a query", () => {
  for (const sort of ["hp", "name", "newest", "oldest"]) {
    load(`?sort=${sort}`);
    assert.equal(effectiveSort(), sort);
    load(`?sort=${sort}&q=pika`);
    assert.equal(effectiveSort(), sort);
  }
});

test("effectiveSort passes an unknown sort through as given", () => {
  load("?sort=nonsense");
  assert.equal(effectiveSort(), "nonsense");
});

test("effectiveOrder is the explicit order when there is one", () => {
  load("?sort=hp&order=asc");
  assert.equal(effectiveOrder(), "asc");
  load("?sort=name&order=desc");
  assert.equal(effectiveOrder(), "desc");
  load("?order=asc");
  assert.equal(effectiveOrder(), "asc");
});

test("effectiveOrder defaults to desc for hp and asc for name", () => {
  load("?sort=hp");
  assert.equal(effectiveOrder(), "desc");
  load("?sort=name");
  assert.equal(effectiveOrder(), "asc");
});

test("effectiveOrder is empty for the sorts that have no default direction", () => {
  assert.equal(effectiveOrder(), "");
  load("?q=pika");
  assert.equal(effectiveOrder(), "");
  load("?sort=newest");
  assert.equal(effectiveOrder(), "");
  load("?sort=relevance");
  assert.equal(effectiveOrder(), "");
});
