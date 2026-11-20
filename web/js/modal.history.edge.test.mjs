import test from "node:test";
import assert from "node:assert/strict";
import {
  cardHashID,
  cardRouteAction,
  cardExitStep,
  deepLinkFailure,
  linkFailureStep,
  cardOpenStep,
} from "./modal.js";
import { entryStateFor } from "./state.js";

const NOT_FOUND = { message: "That card isn't in the archive.", clearHash: true };
const BAD_LINK = { message: "That link does not point to a PokéSearch card.", clearHash: true };
const UNAVAILABLE = { message: "PokéSearch could not load that card. Reload the page to try again.", clearHash: false };

const ODD = [undefined, null, 0, 1, "", "yes", "true", {}, NaN];

// linkFailureStep over every input

test("linkFailureStep never closes the dialog, and with one open always returns the address to that card", () => {
  const failures = [NOT_FOUND, BAD_LINK, UNAVAILABLE, null, undefined, {}, { clearHash: "yes" }];
  const shownCards = ["base1-4", "ex10-?", "cel25c-15_A1", "a&b=c", "a#b", "nope-999"];
  const befores = ["", "#stats", "#card=base1-2", undefined];
  for (const failure of failures) {
    for (const marked of [true, false, ...ODD]) {
      for (const before of befores) {
        for (const shown of shownCards) {
          const step = linkFailureStep({ failure, openCardID: shown, marked, before });
          assert.equal(step.closeDialog, false);
          assert.equal(step.write, "replace", shown);
          assert.equal(cardHashID(step.hash), shown, `the address names ${shown} again: ${step.hash}`);
          assert.equal(step.hash, `#card=${encodeURIComponent(shown)}`, "one spelling, the one openModal writes");
          assert.equal(step.marked, marked === true, `only a real marker is kept: ${String(marked)}`);
        }
        for (const shown of [null, undefined]) {
          const step = linkFailureStep({ failure, openCardID: shown, marked, before });
          assert.equal(step.closeDialog, false);
          assert.equal(step.marked, false, "an entry left with no dialog never carries the marker");
          if (failure?.clearHash) {
            assert.equal(step.write, "replace");
            assert.equal(step.hash, before);
          } else {
            assert.equal(step.write, "none", JSON.stringify(failure));
          }
        }
      }
    }
  }
});

test("a failed link for the very card on screen leaves that card's address", () => {
  // The open card and the failed id can only agree through two spellings of
  // one id; either way the address ends on the card shown.
  const step = linkFailureStep({ failure: UNAVAILABLE, openCardID: "base1-4", marked: true, before: "" });
  assert.deepStrictEqual(step, { closeDialog: false, write: "replace", hash: "#card=base1-4", marked: true });
});

// cardOpenStep over every input

test("cardOpenStep pushes a marked entry only from a non-card address, and never pushes over a card address", () => {
  const hashes = ["", "#", "#stats", "#card=base1-4", "#card=base1%2D4", "#card=BASE1-4", "#card=down-1", "#card=", "#CARD=x"];
  const targets = ["#card=base1-4", "#card=ex10-%3F"];
  const befores = ["", "#stats", undefined];
  for (const hash of hashes) {
    for (const target of targets) {
      for (const marked of [true, false, ...ODD]) {
        for (const before of befores) {
          const step = cardOpenStep({ hash, target, marked, before });
          const label = `${hash} -> ${target} ${String(marked)} ${String(before)}`;
          assert.ok(["push", "replace", "none"].includes(step.write), label);
          assert.equal(typeof step.marked, "boolean", label);
          if (cardHashID(hash) === null) {
            assert.deepStrictEqual(step, { write: "push", marked: true, before: hash }, label);
          } else {
            assert.notEqual(step.write, "push", `${label}: no card address is left beneath`);
            assert.equal(step.marked, marked === true, `${label}: the entry's own marker, nothing more`);
            assert.ok(step.before === before, `${label}: the return address is kept`);
            assert.equal(cardHashID(step.before ?? ""), null, `${label}: the return address is never a card`);
          }
        }
      }
    }
  }
});

test("opening a card whose address is already current in another spelling rewrites it to the one spelling", () => {
  // The address already names the card; rewriting it is harmless and leaves
  // no second entry.
  const step = cardOpenStep({ hash: "#card=base1%2D4", target: "#card=base1-4", marked: false, before: "" });
  assert.equal(step.write, "replace");
  assert.equal(step.marked, false);
});

// A model of session history and of every listener the page binds, built only
// from the exported decisions. Each entry carries its own history.state and
// where it came from, so the test can tell an entry the page pushed from one
// the visitor made. Cards in the grid open at once; any other card is fetched,
// and the answer arrives when the "arrive" move says so.

const MARKER = { cardOverlay: true };
const GRID = ["base1-4", "ex10-?"];

function page({ hash = "", state = null, entries: given } = {}) {
  const entries = given ?? [{ hash, state, origin: "visitor" }];
  let index = entries.length - 1;
  let openCardID = null;
  let addressHash = "";
  let before = "";
  let cached = new Set(GRID);
  let backendUp = false;
  let pending = [];
  const notices = [];
  const current = () => entries[index];
  const marked = () => current().state?.cardOverlay === true;

  function outcome(id) {
    if (id === "down-1") return backendUp ? { status: 200, found: true } : { status: 503, found: false };
    if (id === "late-1") return { status: 200, found: true };
    return { status: 200, found: false };
  }

  function replaceEntry(newHash, newState) {
    entries[index] = { hash: newHash, state: newState, origin: current().origin };
  }

  function closeEvent() {
    openCardID = null;
    const step = cardExitStep({ addressIsCard: cardHashID(current().hash) !== null, pushedByPage: marked() });
    if (step === "back") {
      assert.ok(marked(), "back runs only from a marked entry");
      assert.ok(index > 0, "back never leaves the site");
    }
    if (step === "replace") {
      replaceEntry(before, null);
      addressHash = current().hash;
    }
    return step;
  }

  function show(id) {
    openCardID = id;
  }

  // openLinkedCard after its request, or at once for a card it has.
  function settleLink(id, result) {
    if (cardHashID(current().hash) !== id) return;
    const failure = result;
    if (failure) {
      notices.push(failure.message);
      const step = linkFailureStep({ failure, openCardID, marked: marked(), before });
      if (step.closeDialog) closeModal();
      if (step.write === "replace") {
        replaceEntry(step.hash, step.marked ? MARKER : null);
        addressHash = current().hash;
      }
      return;
    }
    show(id);
  }

  function openLinked(id) {
    if (cached.has(id)) {
      settleLink(id, null);
      return;
    }
    const malformed = deepLinkFailure({ id });
    if (malformed && malformed.message === BAD_LINK.message) {
      settleLink(id, malformed);
      return;
    }
    pending.push(id);
  }

  function closeModal() {
    if (openCardID === null) return "none";
    return closeEvent();
  }

  function sync() {
    const from = addressHash;
    const to = current().hash;
    addressHash = to;
    const result = cardRouteAction({ from, to, openCardID });
    if (result.action === "close") {
      assert.equal(closeModal(), "none", "a close the address caused writes no history");
    } else if (result.action === "open") {
      if (cardHashID(from) === null) before = from;
      openLinked(result.id);
    }
  }

  function traverse(delta) {
    const next = index + delta;
    if (next < 0 || next >= entries.length) return false;
    const hashChanged = entries[next].hash !== current().hash;
    index = next;
    sync();
    if (hashChanged) sync();
    return true;
  }

  function load() {
    openCardID = null;
    pending = [];
    cached = new Set(GRID);
    addressHash = current().hash;
    const id = cardHashID(current().hash);
    if (id === null) return;
    before = "";
    openLinked(id);
  }

  const p = {
    entries,
    current,
    notices,
    get index() { return index; },
    get openCardID() { return openCardID; },
    get pending() { return pending.length; },
    load,
    back: () => traverse(-1),
    forward: () => traverse(1),
    openFromGrid(id) {
      if (openCardID !== null) return false;
      const target = `#card=${encodeURIComponent(id)}`;
      const step = cardOpenStep({ hash: current().hash, target, marked: marked(), before });
      before = step.before;
      const state = step.marked ? MARKER : null;
      if (step.write === "push") {
        entries.splice(index + 1, Infinity, { hash: target, state, origin: "page" });
        index += 1;
      } else if (step.write === "replace") {
        replaceEntry(target, state);
      }
      addressHash = current().hash;
      show(id);
      return true;
    },
    close() {
      if (openCardID === null) return false;
      const start = index;
      if (closeEvent() === "back") traverse(-1);
      assert.ok(start - index <= 1, "one close is at most one step back");
      assert.equal(cardHashID(current().hash), null, `a close never lands on a card address: ${current().hash}`);
      assert.equal(openCardID, null, "nothing opens by itself after a close");
      return true;
    },
    paste(hash) {
      if (hash === current().hash) return false;
      entries.splice(index + 1, Infinity, { hash, state: null, origin: "visitor" });
      index += 1;
      sync();
      sync();
      return true;
    },
    arrive() {
      // The oldest request in flight answers.
      if (!pending.length) return false;
      const id = pending.shift();
      const o = outcome(id);
      const failure = deepLinkFailure({ id, ...o });
      if (!failure) cached.add(id);
      settleLink(id, failure);
      return true;
    },
    recover() {
      if (backendUp) return false;
      backendUp = true;
      return true;
    },
    reload() {
      load();
      return true;
    },
    rewriteSearch() {
      // writeStateToURL({ push: false }): the same address, the state as
      // entryStateFor leaves it.
      entries[index] = { ...current(), state: entryStateFor({ push: false, current: current().state }) };
      return true;
    },
    pushSearch() {
      // A new search from the box or a filter: only with no dialog over them.
      if (openCardID !== null) return false;
      entries.splice(index + 1, Infinity, {
        hash: current().hash,
        state: entryStateFor({ push: true, current: current().state }),
        origin: "page-search",
      });
      index += 1;
      return true;
    },
  };
  return p;
}

function assertHistory(p, path) {
  for (const [i, entry] of p.entries.entries()) {
    if (entry.state?.cardOverlay === true) {
      assert.equal(entry.origin, "page", `${path}: entry ${i} (${entry.hash}) carries the marker but the page never pushed it`);
      assert.notEqual(cardHashID(entry.hash), null, `${path}: entry ${i} is marked but names no card: ${entry.hash}`);
    }
  }
}

function assertSettled(p, path) {
  assertHistory(p, path);
  if (p.pending) return;
  const named = cardHashID(p.current().hash);
  if (p.openCardID !== null) {
    assert.equal(p.openCardID, named, `${path}: the dialog shows ${p.openCardID}, the address names ${named}`);
  } else if (named !== null) {
    assert.equal(named, "down-1", `${path}: the address names ${named} with no dialog`);
  }
}

const MOVES = [
  ["back", (p) => p.back()],
  ["forward", (p) => p.forward()],
  ["open A", (p) => p.openFromGrid("base1-4")],
  ["open B", (p) => p.openFromGrid("ex10-?")],
  ["close", (p) => p.close()],
  ["paste A", (p) => p.paste("#card=base1-4")],
  ["paste A spelled", (p) => p.paste("#card=base1%2D4")],
  ["paste missing", (p) => p.paste("#card=nope-999")],
  ["paste down", (p) => p.paste("#card=down-1")],
  ["paste late", (p) => p.paste("#card=late-1")],
  ["paste stats", (p) => p.paste("#stats")],
  ["arrive", (p) => p.arrive()],
  ["recover", (p) => p.recover()],
  ["reload", (p) => p.reload()],
  ["rewrite search", (p) => p.rewriteSearch()],
  ["push search", (p) => p.pushSearch()],
];

const STARTS = [
  { hash: "" },
  { hash: "#stats" },
  { hash: "#card=base1-4" },
  { hash: "#card=down-1" },
  { hash: "#card=late-1" },
  { hash: "", state: 5 },
  { hash: "#card=base1-4", state: { other: "script" } },
];

test("every sequence of up to four moves, reloads, late answers and recovery included, keeps the dialog and history honest", () => {
  let sequences = 0;
  function explore(replay, path, depth) {
    if (depth === 0) return;
    for (const [name, move] of MOVES) {
      const p = replay();
      const label = `${path} > ${name}`;
      if (!move(p)) continue;
      sequences += 1;
      assertSettled(p, label);
      explore(() => {
        const q = replay();
        move(q);
        return q;
      }, label, depth - 1);
    }
  }
  for (const start of STARTS) {
    const build = () => {
      const p = page(start);
      p.load();
      return p;
    };
    explore(build, JSON.stringify(start), 4);
  }
  assert.ok(sequences > 100_000, `explored ${sequences} sequences`);
});

// A small deterministic generator, so a failing walk can be replayed.
function random(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 2 ** 32;
  };
}

test("long random walks keep the same promises", () => {
  const next = random(20261120);
  for (let walk = 0; walk < 20_000; walk += 1) {
    const start = STARTS[Math.floor(next() * STARTS.length)];
    const p = page(start);
    p.load();
    let path = JSON.stringify(start);
    for (let step = 0; step < 10; step += 1) {
      const [name, move] = MOVES[Math.floor(next() * MOVES.length)];
      if (!move(p)) continue;
      path += ` > ${name}`;
      assertSettled(p, path);
    }
  }
});

test("a reload on a card entry the page pushed, then a close, steps back once to the search beneath", () => {
  const p = page({ hash: "" });
  p.load();
  p.openFromGrid("base1-4");
  p.reload();
  p.rewriteSearch();
  assert.equal(p.openCardID, "base1-4");
  assert.equal(p.current().state?.cardOverlay, true, "the marker survives the first search after a reload");
  p.close();
  assert.equal(p.index, 0);
  assert.equal(p.entries.length, 2, "no duplicate entry");
  assert.equal(p.current().hash, "");
});

test("a link that answers after the visitor opened another card from the grid changes nothing", () => {
  const p = page({ hash: "" });
  p.load();
  p.paste("#card=late-1");
  assert.equal(p.openCardID, null);
  p.openFromGrid("base1-4");
  assert.equal(p.entries.length, 2, "the pending link's entry was taken over, not stacked on");
  p.arrive();
  assert.equal(p.openCardID, "base1-4");
  assert.equal(p.current().hash, "#card=base1-4");
  p.close();
  assert.equal(p.current().hash, "");
});

test("a kept outage link reopens on a reload after the archive is back", () => {
  const p = page({ hash: "#card=down-1" });
  p.load();
  p.arrive();
  assert.equal(p.openCardID, null);
  assert.equal(p.current().hash, "#card=down-1");
  p.recover();
  p.reload();
  p.arrive();
  assert.equal(p.openCardID, "down-1");
  p.close();
  assert.equal(p.current().hash, "");
  assert.equal(p.entries.length, 1, "the visitor stays on the site, in the same entry");
});
