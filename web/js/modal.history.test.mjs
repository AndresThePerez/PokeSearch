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

const NOT_FOUND = { message: "That card isn't in the archive.", clearHash: true };
const UNAVAILABLE = { message: "PokéSearch could not load that card. Reload the page to try again.", clearHash: false };

// linkFailureStep: what a card link that opened nothing does to the address

test("a failed link over an open dialog puts the address back on the card on screen", () => {
  for (const failure of [NOT_FOUND, UNAVAILABLE]) {
    assert.deepStrictEqual(linkFailureStep({ failure, openCardID: "ecard3-146", marked: false, before: "" }), {
      closeDialog: false,
      write: "replace",
      hash: "#card=ecard3-146",
      marked: false,
    }, failure.message);
  }
});

test("putting the address back on the open card keeps the entry's marker as it was", () => {
  assert.deepStrictEqual(linkFailureStep({ failure: UNAVAILABLE, openCardID: "ex10-?", marked: true, before: "#stats" }), {
    closeDialog: false,
    write: "replace",
    hash: "#card=ex10-%3F",
    marked: true,
  });
});

test("a failed link with no dialog open clears a hash that will never open", () => {
  assert.deepStrictEqual(linkFailureStep({ failure: NOT_FOUND, openCardID: null, marked: true, before: "#stats" }), {
    closeDialog: false,
    write: "replace",
    hash: "#stats",
    marked: false,
  });
});

test("a link that could not load with no dialog open keeps its hash for a reload", () => {
  assert.deepStrictEqual(linkFailureStep({ failure: UNAVAILABLE, openCardID: null, marked: false, before: "" }), {
    closeDialog: false,
    write: "none",
    hash: null,
    marked: false,
  });
});

// cardOpenStep: how opening a card from the grid writes history

test("opening a card from a non-card address pushes a marked entry and remembers that address", () => {
  assert.deepStrictEqual(cardOpenStep({ hash: "#stats", target: "#card=base1-4", marked: false, before: "" }), {
    write: "push",
    marked: true,
    before: "#stats",
  });
  assert.deepStrictEqual(cardOpenStep({ hash: "", target: "#card=base1-4", marked: false, before: "#stats" }), {
    write: "push",
    marked: true,
    before: "",
  });
});

test("opening a card over a kept card address replaces it, so no stale card entry is left beneath", () => {
  assert.deepStrictEqual(cardOpenStep({ hash: "#card=base1-2", target: "#card=base1-4", marked: false, before: "" }), {
    write: "replace",
    marked: false,
    before: "",
  });
});

test("replacing a card entry keeps its marker, so closing still steps back to the entry beneath", () => {
  assert.deepStrictEqual(cardOpenStep({ hash: "#card=base1-2", target: "#card=base1-4", marked: true, before: "#stats" }), {
    write: "replace",
    marked: true,
    before: "#stats",
  });
});

test("opening the card the address already names writes nothing", () => {
  assert.deepStrictEqual(cardOpenStep({ hash: "#card=base1-4", target: "#card=base1-4", marked: true, before: "" }), {
    write: "none",
    marked: true,
    before: "",
  });
});

// A model of session history and of the page's listeners, driven only through
// the exported decisions. Cards A and B load; MISSING is not in the archive;
// DOWN never loads (the archive is out for that id).

const OUTCOMES = {
  "base1-4": { status: 200, found: true },
  "ex10-?": { status: 200, found: true },
  "nope-999": { status: 200, found: false },
  "down-1": { networkError: true },
};

function page(startHash, startMarked = false) {
  const entries = [{ hash: startHash, marker: startMarked }];
  let index = 0;
  let openCardID = null;
  let addressHash = startHash;
  let before = "";
  const current = () => entries[index];

  function openLinked(id) {
    const failure = deepLinkFailure({ id, ...(OUTCOMES[id] ?? { status: 200, found: false }) });
    if (!failure) {
      openCardID = id;
      return;
    }
    const step = linkFailureStep({ failure, openCardID, marked: current().marker, before });
    if (step.closeDialog) closeEvent();
    if (step.write === "replace") {
      entries[index] = { hash: step.hash, marker: step.marked };
      addressHash = current().hash;
    }
  }

  function closeEvent() {
    openCardID = null;
    const step = cardExitStep({ addressIsCard: cardHashID(current().hash) !== null, pushedByPage: current().marker });
    if (step === "replace") {
      entries[index] = { hash: before, marker: false };
      addressHash = current().hash;
    }
    return step;
  }

  function sync() {
    const from = addressHash;
    const to = current().hash;
    addressHash = to;
    const result = cardRouteAction({ from, to, openCardID });
    if (result.action === "close") {
      assert.equal(closeEvent(), "none", "a close caused by the address writes nothing");
    } else if (result.action === "open") {
      if (cardHashID(from) === null) before = from;
      openLinked(result.id);
    }
  }

  // popstate and hashchange both arrive for a move that changes the hash.
  function arrive(hashChanged) {
    sync();
    if (hashChanged) sync();
  }

  function traverse(delta) {
    const next = index + delta;
    if (next < 0 || next >= entries.length) return false;
    const hashChanged = entries[next].hash !== current().hash;
    index = next;
    arrive(hashChanged);
    return true;
  }

  const p = {
    entries,
    current,
    get index() { return index; },
    get openCardID() { return openCardID; },
    deepLink() {
      addressHash = current().hash;
      const id = cardHashID(current().hash);
      if (id === null) return;
      before = "";
      openLinked(id);
    },
    back: () => traverse(-1),
    forward: () => traverse(1),
    openFromGrid(id) {
      if (openCardID !== null) return false; // the dialog makes the grid inert
      const target = `#card=${encodeURIComponent(id)}`;
      const step = cardOpenStep({ hash: current().hash, target, marked: current().marker, before });
      if (step.write === "push") {
        entries.splice(index + 1, Infinity, { hash: target, marker: step.marked });
        index += 1;
      } else if (step.write === "replace") {
        entries[index] = { hash: target, marker: step.marked };
      }
      before = step.before;
      addressHash = current().hash;
      openCardID = id;
      return true;
    },
    paste(hash) {
      if (hash === current().hash) return false;
      entries.splice(index + 1, Infinity, { hash, marker: false });
      index += 1;
      arrive(true);
      return true;
    },
    close() {
      if (openCardID === null) return false;
      const start = index;
      if (closeEvent() === "back") traverse(-1);
      assert.ok(start - index <= 1, "one close is never two steps back");
      return true;
    },
  };
  return p;
}

// After every settled move a dialog is open exactly when the address names a
// card, and it shows that card. The one address allowed to name a card with
// no dialog is a link that could not load, kept for a reload to try again.
function assertSettled(p, path) {
  const named = cardHashID(p.current().hash);
  if (p.openCardID !== null) {
    assert.equal(p.openCardID, named, `${path}: the open dialog shows ${p.openCardID} but the address names ${named}`);
  } else if (named !== null) {
    assert.equal(named, "down-1", `${path}: the address names ${named} with no dialog open`);
  }
}

test("a link that is not in the archive, pasted over an open card, leaves that card and its address", () => {
  const p = page("");
  p.openFromGrid("base1-4");
  p.paste("#card=nope-999");
  assert.equal(p.openCardID, "base1-4");
  assert.equal(p.current().hash, "#card=base1-4");
  assertSettled(p, "after the failed paste");
  p.close();
  assert.equal(p.openCardID, null);
  assert.equal(cardHashID(p.current().hash), null, "closing leaves no card address");
});

test("a link that could not load, pasted over an open card, does not leave the address naming another card", () => {
  const p = page("");
  p.openFromGrid("base1-4");
  p.paste("#card=down-1");
  assert.equal(p.openCardID, "base1-4");
  assert.equal(p.current().hash, "#card=base1-4");
});

test("after a deep link failed during an outage, closing a card opened from the grid opens nothing by itself", () => {
  const p = page("#card=down-1");
  p.deepLink();
  assert.equal(p.openCardID, null);
  assert.equal(p.current().hash, "#card=down-1", "the outage keeps the hash");
  p.openFromGrid("ex10-?");
  assert.equal(p.current().hash, "#card=ex10-%3F");
  p.close();
  assert.equal(p.openCardID, null, "nothing opened by itself");
  assert.equal(cardHashID(p.current().hash), null, `the address is ${p.current().hash}`);
  assert.equal(p.entries.length, 1, "no entry was left behind for the failed link");
});

test("a reloaded card entry the page pushed, kept during an outage, still closes by stepping back", () => {
  const p = page("");
  p.openFromGrid("base1-4");
  // The reload: the same marked entry, now failing to load.
  p.entries[p.index] = { hash: "#card=down-1", marker: true };
  const reloaded = page("");
  reloaded.entries.splice(0, Infinity, ...p.entries);
  reloaded.forward();
  assert.equal(reloaded.current().hash, "#card=down-1");
  assert.equal(reloaded.openCardID, null);
  reloaded.openFromGrid("ex10-?");
  reloaded.close();
  assert.equal(reloaded.index, 0, "closing stepped back onto the entry beneath");
  assert.equal(reloaded.current().hash, "");
});

const MOVES = [
  ["back", (p) => p.back()],
  ["forward", (p) => p.forward()],
  ["open A", (p) => p.openFromGrid("base1-4")],
  ["open B", (p) => p.openFromGrid("ex10-?")],
  ["close", (p) => p.close()],
  ["paste A", (p) => p.paste("#card=base1-4")],
  ["paste missing", (p) => p.paste("#card=nope-999")],
  ["paste down", (p) => p.paste("#card=down-1")],
  ["paste malformed", (p) => p.paste("#card=%3Cbad%3E")],
  ["paste stats", (p) => p.paste("#stats")],
  ["paste empty", (p) => p.paste("")],
];

test("every sequence of up to four moves, failing links included, keeps the dialog on the card the address names", () => {
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
  for (const start of ["", "#stats", "#card=base1-4", "#card=nope-999", "#card=down-1"]) {
    const build = () => {
      const p = page(start);
      p.deepLink();
      return p;
    };
    assertSettled(build(), JSON.stringify(start));
    explore(build, JSON.stringify(start), 4);
  }
  assert.ok(sequences > 10000, `explored ${sequences} sequences`);
});

test("closing a card never lands on a card address, not even a kept outage link", () => {
  // A narrower claim than the sweep: what the visitor sees right after the
  // close button, on every path of up to four moves before it.
  function explore(replay, path, depth) {
    if (depth === 0) return;
    for (const [name, move] of MOVES) {
      const p = replay();
      if (!move(p)) continue;
      const label = `${path} > ${name}`;
      const probe = replay();
      move(probe);
      if (probe.close()) {
        assert.equal(cardHashID(probe.current().hash), null, `${label} > close: lands on ${probe.current().hash}`);
        assert.equal(probe.openCardID, null, `${label} > close`);
      }
      explore(() => {
        const q = replay();
        move(q);
        return q;
      }, label, depth - 1);
    }
  }
  for (const start of ["", "#card=down-1", "#card=base1-4"]) {
    explore(() => {
      const p = page(start);
      p.deepLink();
      return p;
    }, JSON.stringify(start), 4);
  }
});
