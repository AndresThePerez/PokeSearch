import test from "node:test";
import assert from "node:assert/strict";
import { commitQuery, searchBoxStatus, scheduleSearch, cancelScheduledSearch } from "./api.js";
import { readStateFromURL, state } from "./state.js";

// commitQuery's return value, which the focus step after Enter waits on: null
// when the commit starts no search, a promise when it starts one. runSearch
// itself needs the page, so what it resolves to is not reached here; the
// promise is only observed, and its rejection (there is no page) is taken.

const writes = [];

function at(search) {
  globalThis.location = { search, pathname: "/", hash: "" };
  globalThis.history = {
    state: null,
    pushState: (_state, _title, url) => writes.push(["push", url]),
    replaceState: (_state, _title, url) => writes.push(["replace", url]),
    back: () => writes.push(["back"]),
  };
  readStateFromURL();
  writes.length = 0;
}

test.afterEach(() => {
  cancelScheduledSearch();
  at("");
  delete globalThis.location;
  delete globalThis.history;
});

test("a commit for the query the address already holds starts no search and returns null", () => {
  for (const trigger of ["enter", "blur", "idle"]) {
    at("?q=pika");
    assert.equal(commitQuery(trigger), null, trigger);
    assert.deepStrictEqual(writes, [], `${trigger}: nothing written`);
  }
  at("");
  assert.equal(commitQuery("enter"), null, "an empty box on an empty address");
  state.q = "   ";
  assert.equal(commitQuery("enter"), null, "spaces are an empty query");
});

test("a commit for a different query starts one search and returns it", async () => {
  at("?q=pika");
  state.q = "pikac";
  const search = commitQuery("enter");
  assert.ok(search instanceof Promise, "a promise the focus step can wait on");
  await search.catch(() => {}); // there is no page to paint
  assert.equal(writes.filter(([kind]) => kind === "push" || kind === "replace").length, 1, "one history write, not two");
  assert.match(writes.find(([kind]) => kind === "push" || kind === "replace")[1], /q=pikac/);
});

test("a commit runs the waiting keystroke search itself, so the debounce is empty afterwards", async () => {
  at("?q=pika");
  state.q = "pikac";
  scheduleSearch();
  assert.equal(searchBoxStatus().pending, true);
  const search = commitQuery("enter");
  assert.ok(search instanceof Promise);
  assert.equal(searchBoxStatus().pending, false, "the debounce was cleared: the keystroke search does not run a second time");
  await search.catch(() => {});
  assert.equal(writes.filter(([kind]) => kind === "push" || kind === "replace").length, 1);
});

test("a second commit for the same query, once the first has written it, starts nothing", async () => {
  at("?q=pika");
  state.q = "pikac";
  await commitQuery("enter").catch(() => {});
  // The address now holds the query: the stub's location follows the write.
  const written = writes.find(([kind]) => kind === "push" || kind === "replace")[1];
  globalThis.location = { search: new URL(written, "http://127.0.0.1").search, pathname: "/", hash: "" };
  writes.length = 0;
  assert.equal(commitQuery("enter"), null);
  assert.equal(commitQuery("blur"), null, "the blur that follows Enter's focus move");
  assert.deepStrictEqual(writes, []);
});
