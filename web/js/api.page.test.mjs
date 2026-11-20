import test from "node:test";
import assert from "node:assert/strict";
import { SEARCH_PAGE_SIZE, searchRequestParams, stepBackHolds } from "./api.js";
import { readStateFromURL, state } from "./state.js";

function at(search) {
  globalThis.location = { search, pathname: "/", hash: "" };
  readStateFromURL();
}

test.afterEach(() => {
  at("");
  delete globalThis.location;
});

// searchRequestParams: the querystring of the search request

test("the search request asks for the page size the page draws its skeleton for", () => {
  at("?q=pika");
  assert.equal(searchRequestParams().get("page_size"), String(SEARCH_PAGE_SIZE));
  assert.equal(SEARCH_PAGE_SIZE, 24, "the server's own default, so no response changes");
});

test("the search request keeps the query, the page and the debug flag", () => {
  at("?q=pika&types=Fire,Water&sort=hp");
  state.page = 3;
  const sp = searchRequestParams({ debug: true });
  assert.equal(sp.get("q"), "pika");
  assert.equal(sp.get("types"), "Fire,Water");
  assert.equal(sp.get("sort"), "hp");
  assert.equal(sp.get("page"), "3");
  assert.equal(sp.get("debug"), "1");
  assert.equal(searchRequestParams().get("debug"), null);
});

// stepBackHolds: a commit while the page's own step back is under way

test("a commit for the query a step back is heading to has nothing to write", () => {
  assert.equal(stepBackHolds({ stepBackTo: "q=pika", wanted: "q=pika" }), true);
  assert.equal(stepBackHolds({ stepBackTo: "", wanted: "" }), true, "the step back to a browse");
});

test("a commit for another query, or with no step back under way, goes ahead", () => {
  assert.equal(stepBackHolds({ stepBackTo: "q=pika", wanted: "q=pikachu" }), false);
  assert.equal(stepBackHolds({ stepBackTo: null, wanted: "q=pika" }), false);
  assert.equal(stepBackHolds({ stepBackTo: null, wanted: "" }), false);
});
