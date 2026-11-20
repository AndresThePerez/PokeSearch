import test from "node:test";
import assert from "node:assert/strict";
import { entryStateFor, writeStateToURL, readStateFromURL, state } from "./state.js";

const MARKER = { cardOverlay: true };

test("a replace passes any state through untouched, ours or not, and a push never carries one", () => {
  const foreign = [MARKER, { other: "script" }, { cardOverlay: "true" }, 5, 0, "", "card", false, true, [], Object.create(null)];
  for (const [i, current] of foreign.entries()) {
    assert.ok(entryStateFor({ push: false, current }) === current, `replace keeps state ${i}`);
    assert.equal(entryStateFor({ push: true, current }), null, `push drops state ${i}`);
  }
  for (const current of [null, undefined]) {
    assert.equal(entryStateFor({ push: false, current }), null);
    assert.equal(entryStateFor({ push: true, current }), null);
  }
  // Anything truthy but not true is not a push.
  assert.ok(entryStateFor({ push: undefined, current: MARKER }) === MARKER);
});

// A plain stand-in for the session history: entries with their own state, and
// a location that follows the current entry.
function browser(url, entryState) {
  const entries = [{ url, state: entryState }];
  let index = 0;
  const parse = (u) => {
    const parsed = new URL(u, "http://127.0.0.1");
    return { pathname: parsed.pathname, search: parsed.search, hash: parsed.hash };
  };
  globalThis.location = parse(url);
  const follow = () => Object.assign(globalThis.location, parse(entries[index].url));
  globalThis.history = {
    get state() { return entries[index].state; },
    pushState(s, _t, u) {
      entries.splice(index + 1, Infinity, { url: u, state: s });
      index += 1;
      follow();
    },
    replaceState(s, _t, u) {
      entries[index] = { url: u, state: s };
      follow();
    },
    back() {
      if (index > 0) index -= 1;
      follow();
    },
  };
  readStateFromURL();
  return { entries, get index() { return index; } };
}

test("a search write never invents the marker, never copies it onto a new entry, and keeps the hash", () => {
  // Every combination of address, state and write: the marker is written only
  // over the entry that already had it, at the same address.
  for (const hash of ["", "#stats", "#card=base1-4"]) {
    for (const entryState of [MARKER, null, 7, { other: 1 }]) {
      for (const push of [false, true]) {
        for (const nextQuery of ["pikachu", "raichu"]) {
          const b = browser(`/?q=pikachu${hash}`, entryState);
          state.q = nextQuery;
          assert.doesNotThrow(() => writeStateToURL({ push }));
          const written = b.entries[b.index];
          assert.ok(written.url.endsWith(hash) || hash === "", `${written.url} keeps ${hash}`);
          if (written.state === MARKER) {
            assert.equal(b.entries.length, 1, "the marker is never copied onto a new entry");
            assert.ok(entryState === MARKER, "the marker is never invented");
          }
          if (b.entries.length === 2) assert.equal(written.state, null, "a pushed search starts clean");
        }
      }
    }
  }
});

test("a typing burst from a marked card entry leaves the marker where it was, and the step back lands on it", () => {
  // A card link kept during an outage, reloaded on an entry the page pushed:
  // no dialog covers the box, so the visitor can type.
  const b = browser("/?q=pikachu#card=base1-4", MARKER);
  state.q = "pikachux";
  assert.equal(writeStateToURL({ push: true }), true, "the burst's own entry");
  assert.equal(b.entries.length, 2);
  assert.equal(b.entries[1].state, null, "the draft entry is not a card entry of the page's");
  writeStateToURL();
  assert.equal(b.entries[1].state, null, "rewriting the draft keeps it unmarked");
  assert.ok(b.entries[0].state === MARKER, "the entry beneath keeps its marker");
  // The page's own step back to the query beneath the draft.
  history.back();
  assert.equal(b.index, 0);
  assert.ok(history.state === MARKER);
  readStateFromURL();
  writeStateToURL();
  assert.ok(b.entries[0].state === MARKER, "and a rewrite after the step back keeps it too");
  assert.equal(b.entries[0].url, "/?q=pikachu#card=base1-4");
});

test("a search rewrite after a reload on a marked card entry keeps it, and a filter push after the close starts clean", () => {
  const b = browser("/?q=pikachu#card=base1-4", MARKER);
  // The first search of the reload rewrites the address in place.
  writeStateToURL();
  assert.ok(b.entries[0].state === MARKER);
  // Closing steps back off it (not modelled here); on a fresh, unmarked
  // entry a filter search pushes and carries nothing.
  const c = browser("/?q=pikachu", null);
  state.types = ["Fire"];
  writeStateToURL({ push: true });
  assert.equal(c.entries.length, 2);
  assert.equal(c.entries[1].state, null);
  state.types = [];
});
