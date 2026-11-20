import test from "node:test";
import assert from "node:assert/strict";
import { entryStateFor, writeStateToURL, readStateFromURL, state } from "./state.js";

// entryStateFor: what an address rewrite leaves in history.state

test("a replace keeps the current entry's state", () => {
  const current = { cardOverlay: true };
  assert.ok(entryStateFor({ push: false, current }) === current, "the same object is kept");
});

test("a push starts an entry with no state", () => {
  assert.equal(entryStateFor({ push: true, current: { cardOverlay: true } }), null);
});

test("a replace over an entry with no state writes none", () => {
  assert.equal(entryStateFor({ push: false, current: null }), null);
  assert.equal(entryStateFor({ push: false, current: undefined }), null);
});

// writeStateToURL, with plain stand-ins for location and history

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

test("a search rewriting a card entry the page pushed keeps its marker", () => {
  const marker = { cardOverlay: true };
  const calls = stage("?q=pikachu", "#card=base1-58", marker);
  assert.equal(writeStateToURL(), false);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].op, "replace");
  assert.equal(calls[0].url, "/?q=pikachu#card=base1-58");
  assert.ok(calls[0].state === marker, "the marker survives the rewrite");
});

test("a pushed search starts an entry without the marker", () => {
  const calls = stage("?q=pikachu", "", { cardOverlay: true });
  state.q = "raichu";
  assert.equal(writeStateToURL({ push: true }), true);
  assert.deepStrictEqual(calls, [{ op: "push", state: null, url: "/?q=raichu" }]);
});

test("a push of an unchanged address is a replace and keeps the state too", () => {
  const marker = { cardOverlay: true };
  const calls = stage("?q=pikachu", "#card=base1-58", marker);
  assert.equal(writeStateToURL({ push: true }), false);
  assert.equal(calls[0].op, "replace");
  assert.ok(calls[0].state === marker);
});
