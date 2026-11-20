import test from "node:test";
import assert from "node:assert/strict";
import {
  cardHashID,
  cardRouteAction,
  cardExitStep,
  deepLinkFailure,
  explainButtonView,
  energyIcon,
} from "./modal.js";

// The server's card id shape (search.CardIDPattern), copied here on purpose:
// the page and the API must agree on which ids are worth asking for.
const SERVER_CARD_ID = /^[A-Za-z0-9][A-Za-z0-9._!?-]{0,127}$/;

const BAD_CARD_LINK = "That link does not point to a PokéSearch card.";
const CARD_NOT_FOUND = "That card isn't in the archive.";
const CARD_UNAVAILABLE = "PokéSearch could not load that card. Reload the page to try again.";

// cardHashID

test("cardHashID reads every shape of hash without throwing, and only #card= names a card", () => {
  const long = "x".repeat(10_000);
  const cases = [
    ["", null],
    ["#", null],
    ["#card", null],
    ["#card=", ""],
    ["#card=base1-4", "base1-4"],
    ["#CARD=base1-4", null],
    ["#Card=base1-4", null],
    ["#card=a%20b", "a b"],
    ["#card=%E0%A4%A", "%E0%A4%A"],
    ["#card=%", "%"],
    ["#card=x#card=y", "x#card=y"],
    ["#card=x&foo=1", "x&foo=1"],
    ["#stats", null],
    ["#statscard=x", null],
    ["#xcard=base1-4", null],
    [" #card=base1-4", null],
    ["#card=" + long, long],
    ["#card=%00", "\u0000"],
    ["#card=a%2Fb", "a/b"],
    ["#card=ex10-?", "ex10-?"],
    ["#card=%3Cimg%3E", "<img>"],
    [undefined, null],
    [null, null],
    [42, null],
  ];
  for (const [hash, want] of cases) {
    let got;
    assert.doesNotThrow(() => { got = cardHashID(hash); }, String(hash).slice(0, 40));
    assert.equal(got, want, String(hash).slice(0, 40));
  }
});

test("every real card id round-trips through the link the dialog writes and is not called a bad link", () => {
  // The corpus holds ids with an underscore, a bang and a question mark; a
  // link to any of them must reach the API, not be refused by the page.
  const ids = ["base1-4", "cel25c-15_A1", "cel25c-17_A", "ex10-!", "ex10-?", "swsh45sv-SV107", "pop5-17", "xy7-4a"];
  for (const id of ids) {
    assert.ok(SERVER_CARD_ID.test(id), `${id} is a server id`);
    const hash = `#card=${encodeURIComponent(id)}`;
    assert.equal(cardHashID(hash), id, hash);
    assert.equal(deepLinkFailure({ id: cardHashID(hash), status: 200, found: true, networkError: false }), null, id);
    const missing = deepLinkFailure({ id, status: 200, found: false, networkError: false });
    assert.equal(missing.message, CARD_NOT_FOUND, `${id} is well formed, so missing means not in the archive`);
  }
});

test("the page and the server agree on which hashes hold a well-formed id", () => {
  // Any id the page would send must be one the server accepts, and any id the
  // server accepts must not be reported as a malformed link.
  const ids = ["", "a", "A".repeat(128), "A".repeat(129), "-a", ".a", "_a", "a_", "a b", "a/b", "a\u0000", "a\n",
    "é", "ex10-?", "ex10-!", "a.b", "x<y", "%41", "a+b", "a%2Fb", "\u{1F600}"];
  for (const id of ids) {
    const failure = deepLinkFailure({ id, status: 200, found: false, networkError: false });
    const pageCallsItMalformed = failure.message === BAD_CARD_LINK;
    assert.equal(pageCallsItMalformed, !SERVER_CARD_ID.test(id), JSON.stringify(id).slice(0, 40));
  }
});

// cardRouteAction and cardExitStep, driven as the page drives them

// A model of the browser's session history and of the listeners the page
// binds: openModal from the grid, the dialog's close event, and the one
// handler popstate and hashchange both call. Every card is in the grid, so
// an open lands at once.
function page(startHash) {
  const entries = [{ hash: startHash, marker: false }];
  let index = 0;
  let openCardID = null;
  let addressHash = startHash;
  let hashBeforeModal = "";
  const log = [];
  const current = () => entries[index];

  function closeEvent() {
    openCardID = null;
    const step = cardExitStep({ addressIsCard: cardHashID(current().hash) !== null, pushedByPage: current().marker });
    assert.ok(["back", "replace", "none"].includes(step), step);
    if (step === "back") {
      assert.ok(current().marker, "only an entry the page pushed is stepped back from");
      return { step, traverse: -1 };
    }
    if (step === "replace") {
      entries[index] = { hash: hashBeforeModal, marker: false };
      addressHash = current().hash;
    }
    return { step, traverse: 0 };
  }

  function sync() {
    const from = addressHash;
    const to = current().hash;
    addressHash = to;
    const result = cardRouteAction({ from, to, openCardID });
    assert.ok(["open", "close", "nothing"].includes(result.action), JSON.stringify(result));
    if (result.action === "close") {
      // closeModal fires the close event; Back already moved the address.
      const { step } = closeEvent();
      assert.equal(step, "none", `a close caused by ${from} -> ${to} writes no history`);
    } else if (result.action === "open") {
      if (cardHashID(from) === null) hashBeforeModal = from;
      openCardID = result.id;
    }
    return result;
  }

  // Deliver the events one move fires, in the order and number given.
  function deliver(order) {
    const results = order.map(() => sync());
    for (const extra of results.slice(1)) {
      assert.equal(extra.action, "nothing", "the second event of one move is a no-op");
    }
  }

  function traverse(delta, order) {
    const next = index + delta;
    if (next < 0 || next >= entries.length) return false;
    const hashChanged = entries[next].hash !== current().hash;
    index = next;
    deliver(hashChanged ? order : order.filter((e) => e === "popstate"));
    return true;
  }

  return {
    log,
    get openCardID() { return openCardID; },
    get index() { return index; },
    current,
    entries,
    deepLink() {
      // openDeepLink on a page loaded at a card address.
      const id = cardHashID(startHash);
      if (id !== null) {
        hashBeforeModal = "";
        openCardID = id;
      }
    },
    back(order) { return traverse(-1, order); },
    forward(order) { return traverse(1, order); },
    openFromGrid(id) {
      if (openCardID !== null) return false; // the dialog makes the grid inert
      if (cardHashID(current().hash) === null) hashBeforeModal = current().hash;
      const hash = `#card=${encodeURIComponent(id)}`;
      if (current().hash !== hash) {
        entries.splice(index + 1, Infinity, { hash, marker: true });
        index += 1;
      }
      addressHash = current().hash;
      openCardID = id;
      return true;
    },
    paste(hash, order) {
      if (hash === current().hash) return false;
      entries.splice(index + 1, Infinity, { hash, marker: false });
      index += 1;
      deliver(order);
      return true;
    },
    closeByButton(order) {
      if (openCardID === null) return false;
      const before = index;
      const { traverse: delta } = closeEvent();
      if (delta) traverse(delta, order);
      assert.ok(before - index <= 1, "one close is never two steps back");
      assert.equal(cardHashID(current().hash), null, `a close never leaves a card address current: ${current().hash}`);
      return true;
    },
  };
}

const ORDERS = [["popstate", "hashchange"], ["hashchange", "popstate"], ["popstate"], ["hashchange"]];
const MOVES = [
  ["back", (p, o) => p.back(o)],
  ["forward", (p, o) => p.forward(o)],
  ["open A", (p) => p.openFromGrid("base1-4")],
  ["open B", (p) => p.openFromGrid("ex10-?")],
  ["close", (p, o) => p.closeByButton(o)],
  ["paste A", (p, o) => p.paste("#card=base1-4", o)],
  ["paste B", (p, o) => p.paste("#card=ex10-%3F", o)],
  ["paste stats", (p, o) => p.paste("#stats", o)],
  ["paste empty", (p, o) => p.paste("", o)],
];

function settled(p, path) {
  const id = cardHashID(p.current().hash);
  assert.equal(p.openCardID, id, `${path}: the dialog shows exactly the card the address names`);
}

test("every sequence of up to four navigations leaves the dialog on the card the address names", () => {
  let sequences = 0;
  function explore(build, path, depth) {
    if (depth === 0) return;
    for (const [name, move] of MOVES) {
      for (const order of ORDERS) {
        if ((name === "open A" || name === "open B") && order !== ORDERS[0]) continue;
        const p = build();
        const label = `${path} > ${name} [${order.join(",")}]`;
        if (!move(p, order)) continue;
        sequences += 1;
        settled(p, label);
        const replay = () => {
          const q = build();
          move(q, order);
          return q;
        };
        explore(replay, label, depth - 1);
      }
    }
  }
  for (const start of ["", "#stats", "#card=base1-4"]) {
    const build = () => {
      const p = page(start);
      p.deepLink();
      return p;
    };
    settled(build(), start);
    explore(build, JSON.stringify(start), 4);
  }
  assert.ok(sequences > 1000, `explored ${sequences} sequences`);
});

test("Forward onto another card while one is open switches the dialog without stepping history", () => {
  const p = page("");
  p.openFromGrid("base1-4");
  p.back(ORDERS[0]);
  p.paste("#card=ex10-%3F", ORDERS[0]);
  p.back(ORDERS[0]);
  assert.equal(p.openCardID, null);
  p.forward(ORDERS[0]);
  assert.equal(p.openCardID, "ex10-?");
  // Now a card is open; edit the address onto the other card.
  const before = p.index;
  p.paste("#card=base1-4", ORDERS[1]);
  assert.equal(p.openCardID, "base1-4");
  assert.equal(p.index, before + 1, "the visitor's own navigation is the only step");
});

test("cardRouteAction treats an undefined open card as a closed dialog and never closes nothing", () => {
  assert.deepStrictEqual(cardRouteAction({ from: "#card=a", to: "", openCardID: undefined }), { action: "nothing" });
  assert.deepStrictEqual(cardRouteAction({ from: "", to: "#card=a", openCardID: undefined }), { action: "open", id: "a" });
  // Two spellings of one id are one card: no second open.
  assert.deepStrictEqual(cardRouteAction({ from: "#card=ex10-%3F", to: "#card=ex10-?", openCardID: "ex10-?" }), { action: "nothing" });
  // A hash that is not a card address over an open dialog closes it, whatever it is.
  for (const to of ["", "#", "#stats", "#CARD=a", "#cards=a"]) {
    assert.deepStrictEqual(cardRouteAction({ from: "#card=a", to, openCardID: "a" }), { action: "close" }, to);
  }
});

test("cardExitStep steps back only from an entry the page pushed that is still a card address", () => {
  for (const addressIsCard of [true, false, undefined, null, 0, 1, "yes"]) {
    for (const pushedByPage of [true, false, undefined, null, 0, 1, "yes"]) {
      const step = cardExitStep({ addressIsCard, pushedByPage });
      assert.ok(["back", "replace", "none"].includes(step), `${addressIsCard} ${pushedByPage}: ${step}`);
      if (!addressIsCard) assert.equal(step, "none", `${addressIsCard} ${pushedByPage}`);
      if (step === "back") assert.ok(addressIsCard && pushedByPage, `${addressIsCard} ${pushedByPage}`);
    }
  }
});

// deepLinkFailure

test("deepLinkFailure never throws, never echoes the id, and sorts every outcome into one of three verdicts", () => {
  const ids = ["base1-4", "ex10-?", "cel25c-17_A", "", "<img src=x onerror=alert(1)>", "%E0%A4%A", "a b",
    "x".repeat(10_000), "nope-999", undefined, null, 42, {}];
  const statuses = [undefined, 0, 200, 204, 301, 400, 401, 403, 404, 408, 410, 422, 429, 499, 500, 502, 503, 504, NaN];
  const founds = [true, false, undefined];
  const messages = new Set([BAD_CARD_LINK, CARD_NOT_FOUND, CARD_UNAVAILABLE]);
  for (const id of ids) {
    const wellFormed = typeof id === "string" && SERVER_CARD_ID.test(id);
    for (const status of statuses) {
      for (const found of founds) {
        for (const networkError of [true, false]) {
          const label = `${String(id).slice(0, 20)} ${status} ${found} ${networkError}`;
          let failure;
          assert.doesNotThrow(() => { failure = deepLinkFailure({ id, status, found, networkError }); }, label);
          if (failure === null) {
            assert.ok(wellFormed && found === true && !networkError, `${label}: only a found card is no failure`);
            continue;
          }
          assert.ok(messages.has(failure.message), `${label}: ${failure.message}`);
          assert.equal(typeof failure.clearHash, "boolean", label);
          if (typeof id === "string" && id !== "") {
            assert.ok(!failure.message.includes(id), `${label}: the id is not echoed`);
          }
          if (!wellFormed) {
            assert.deepStrictEqual(failure, { message: BAD_CARD_LINK, clearHash: true }, label);
          } else if (networkError || status >= 500) {
            assert.deepStrictEqual(failure, { message: CARD_UNAVAILABLE, clearHash: false }, `${label}: an outage keeps the hash`);
          } else if (status === 404 || (status >= 200 && status < 300 && found !== true)) {
            assert.deepStrictEqual(failure, { message: CARD_NOT_FOUND, clearHash: true }, label);
          }
        }
      }
    }
  }
});

test("deepLinkFailure with only an id is the verdict before any request", () => {
  assert.deepStrictEqual(deepLinkFailure({ id: "<script>" }), { message: BAD_CARD_LINK, clearHash: true });
  // A well-formed id with no answer yet is treated as missing: openLinkedCard
  // always replaces it with the real outcome before reporting.
  assert.deepStrictEqual(deepLinkFailure({ id: "base1-4" }), { message: CARD_NOT_FOUND, clearHash: true });
});

// explainButtonView

test("explainButtonView gives a safe, consistent view for any status, known or not", () => {
  const known = ["idle", "open", "loading", "failed"];
  const odd = [undefined, null, "", "OPEN", "Failed", " loading", "__proto__", "constructor", "toString",
    "hasOwnProperty", 0, 1, {}, [], NaN];
  for (const status of [...known, ...odd]) {
    let view;
    assert.doesNotThrow(() => { view = explainButtonView(status); }, String(status));
    assert.equal(typeof view.label, "string", String(status));
    assert.ok(view.label.length > 0, String(status));
    assert.equal(typeof view.expanded, "boolean", String(status));
    assert.equal(typeof view.busy, "boolean", String(status));
  }
  for (const status of odd) {
    assert.deepStrictEqual(explainButtonView(status), explainButtonView("idle"), String(status));
  }
  const labels = known.map((s) => explainButtonView(s).label);
  assert.equal(new Set(labels).size, 4, labels.join(" | "));
  assert.equal(explainButtonView("loading").busy, true);
  for (const s of ["idle", "open", "failed"]) assert.equal(explainButtonView(s).busy, false, s);
  assert.equal(explainButtonView("failed").label, "Try again");
  assert.ok(!/hide/i.test(explainButtonView("failed").label), "a failed breakdown never offers to hide it");
});

test("explainButtonView returns a fresh view each call, so a caller cannot corrupt the next one", () => {
  const first = explainButtonView("failed");
  first.label = "tampered";
  first.busy = true;
  assert.equal(explainButtonView("failed").label, "Try again");
  assert.equal(explainButtonView("failed").busy, false);
});

// energyIcon

const DISPLAY = {
  Grass: "Grass", Fire: "Fire", Water: "Water", Lightning: "Lightning", Psychic: "Psychic", Fighting: "Fighting",
  Darkness: "Darkness", Metal: "Steel", Fairy: "Fairy", Dragon: "Dragon", Colorless: "Normal",
};

test("the eleven energy types give eleven distinct one-character glyphs and distinct classes", () => {
  const glyphs = new Set();
  const classes = new Set();
  for (const [type, shown] of Object.entries(DISPLAY)) {
    const icon = energyIcon(type);
    assert.equal([...icon.glyph].length, 1, type);
    assert.notEqual(icon.glyph, "?", type);
    assert.equal(icon.name, `${shown} energy`, type);
    assert.equal(icon.className, `energy-icon energy-${type.toLowerCase()}`, type);
    glyphs.add(icon.glyph);
    classes.add(icon.className);
  }
  assert.equal(glyphs.size, 11);
  assert.equal(classes.size, 11);
});

test("an energy type the page does not know never throws, never borrows an inherited glyph and never adds a class", () => {
  const odd = ["fire", "FIRE", " Fire", "Fire ", "x y", "fire onload=alert(1)", "\"><img src=x>", "__proto__",
    "constructor", "toString", "hasOwnProperty", "valueOf", "", "x".repeat(1000), "Plasma", undefined, null, 7, {},
    [], ["Fire"], new String("Fire")];
  for (const type of odd) {
    const label = typeof type === "string" ? JSON.stringify(type.slice(0, 30)) : String(type);
    let icon;
    assert.doesNotThrow(() => { icon = energyIcon(type); }, label);
    assert.equal(icon.glyph, "?", label);
    assert.equal(typeof icon.glyph, "string", label);
    assert.equal(icon.className, "energy-icon", `${label}: only the neutral class`);
    assert.equal(typeof icon.name, "string", label);
    assert.ok(icon.name.endsWith(" energy"), label);
    assert.ok(icon.name.length > " energy".length, `${label}: the name is never empty`);
    if (typeof type !== "string") assert.equal(icon.name, "Unknown energy", label);
  }
});
