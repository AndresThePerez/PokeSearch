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

// cardHashID: reading a card id out of the address

test("a #card= hash names the decoded card id", () => {
  assert.equal(cardHashID("#card=base1-4"), "base1-4");
  assert.equal(cardHashID("#card=ex10-%3F"), "ex10-?");
});

test("a hash that is not a card address names no card", () => {
  for (const hash of ["", "#", "#stats", "#cards=base1-4", "card=base1-4"]) {
    assert.equal(cardHashID(hash), null, hash);
  }
});

test("a card hash that cannot be decoded is returned as written, not thrown", () => {
  assert.equal(cardHashID("#card=%E0%A4%A"), "%E0%A4%A");
  assert.equal(cardHashID("#card="), "");
});

// cardRouteAction: what a change of address means for the dialog

test("Back from a card address closes the open dialog", () => {
  assert.deepStrictEqual(cardRouteAction({ from: "#card=base1-4", to: "", openCardID: "base1-4" }), { action: "close" });
  assert.deepStrictEqual(cardRouteAction({ from: "#card=base1-4", to: "#stats", openCardID: "base1-4" }), { action: "close" });
});

test("Forward or a pasted link onto a card address opens that card", () => {
  assert.deepStrictEqual(cardRouteAction({ from: "", to: "#card=base1-4", openCardID: null }), { action: "open", id: "base1-4" });
  assert.deepStrictEqual(cardRouteAction({ from: "#stats", to: "#card=base1-4", openCardID: null }), { action: "open", id: "base1-4" });
});

test("a card address for another card than the open one opens the new card", () => {
  assert.deepStrictEqual(cardRouteAction({ from: "#card=base1-4", to: "#card=base1-5", openCardID: "base1-4" }), { action: "open", id: "base1-5" });
});

test("the address of the card already open changes nothing", () => {
  assert.deepStrictEqual(cardRouteAction({ from: "", to: "#card=base1-4", openCardID: "base1-4" }), { action: "nothing" });
});

test("an address that did not change changes nothing, whatever is open", () => {
  // popstate and hashchange both fire for one fragment navigation; the
  // second one must not open the card a second time or close it.
  assert.deepStrictEqual(cardRouteAction({ from: "#card=base1-4", to: "#card=base1-4", openCardID: null }), { action: "nothing" });
  assert.deepStrictEqual(cardRouteAction({ from: "", to: "", openCardID: "base1-4" }), { action: "nothing" });
});

test("moving between non-card addresses with no dialog open changes nothing", () => {
  assert.deepStrictEqual(cardRouteAction({ from: "", to: "#stats", openCardID: null }), { action: "nothing" });
  assert.deepStrictEqual(cardRouteAction({ from: "#stats", to: "", openCardID: null }), { action: "nothing" });
});

// cardExitStep: how closing the dialog leaves its card address

test("closing a card whose entry this page pushed steps back once", () => {
  assert.equal(cardExitStep({ addressIsCard: true, pushedByPage: true }), "back");
});

test("closing a card opened from a pasted link removes the hash in place", () => {
  assert.equal(cardExitStep({ addressIsCard: true, pushedByPage: false }), "replace");
});

test("a close that Back already caused writes nothing, so one close is never two steps back", () => {
  assert.equal(cardExitStep({ addressIsCard: false, pushedByPage: true }), "none");
  assert.equal(cardExitStep({ addressIsCard: false, pushedByPage: false }), "none");
});

// deepLinkFailure: how a failed #card= link is reported

test("a malformed card id says so and clears the hash", () => {
  for (const id of ["", "<script>", "-leading-dash", "a b", "x".repeat(129)]) {
    const failure = deepLinkFailure({ id, status: 0, found: false, networkError: false });
    assert.equal(failure.clearHash, true, id);
    assert.equal(failure.message, "That link does not point to a PokéSearch card.", id);
  }
});

test("a well-formed id the archive does not hold says the card is not in the archive", () => {
  for (const outcome of [{ status: 200, found: false }, { status: 404, found: false }]) {
    assert.deepStrictEqual(deepLinkFailure({ id: "nope-999", networkError: false, ...outcome }), {
      message: "That card isn't in the archive.",
      clearHash: true,
    });
  }
});

test("a refused id (400) is reported as a bad link and cleared", () => {
  assert.deepStrictEqual(deepLinkFailure({ id: "nope-999", status: 400, found: false, networkError: false }), {
    message: "That link does not point to a PokéSearch card.",
    clearHash: true,
  });
});

test("a network failure or a 5xx keeps the hash and says the card could not be loaded", () => {
  const expected = { message: "PokéSearch could not load that card. Reload the page to try again.", clearHash: false };
  assert.deepStrictEqual(deepLinkFailure({ id: "base1-4", status: 0, found: false, networkError: true }), expected);
  for (const status of [500, 502, 503]) {
    assert.deepStrictEqual(deepLinkFailure({ id: "base1-4", status, found: false, networkError: false }), expected, String(status));
  }
});

test("a card that was found is no failure", () => {
  assert.equal(deepLinkFailure({ id: "base1-4", status: 200, found: true, networkError: false }), null);
});

// explainButtonView: the score breakdown button in each state

test("the breakdown button reads as a question until a breakdown is shown", () => {
  assert.deepStrictEqual(explainButtonView("idle"), { label: "Why this card?", expanded: false, busy: false });
  assert.deepStrictEqual(explainButtonView("open"), { label: "Hide the breakdown", expanded: true, busy: false });
  assert.deepStrictEqual(explainButtonView("loading"), { label: "Explaining…", expanded: false, busy: true });
});

test("a failed breakdown offers to try again rather than to hide it", () => {
  const view = explainButtonView("failed");
  assert.equal(view.label, "Try again");
  assert.equal(view.busy, false);
  assert.notEqual(view.label, explainButtonView("open").label);
});

// energyIcon: a glyph and an accessible name per energy type

const ENERGY = {
  Grass: "G", Fire: "R", Water: "W", Lightning: "L", Psychic: "P", Fighting: "F",
  Darkness: "D", Metal: "M", Fairy: "Y", Dragon: "N", Colorless: "C",
};

test("each energy type has the trading card game's own letter", () => {
  for (const [type, glyph] of Object.entries(ENERGY)) {
    assert.equal(energyIcon(type).glyph, glyph, type);
  }
});

test("the eleven energy glyphs are all different", () => {
  const glyphs = Object.keys(ENERGY).map((type) => energyIcon(type).glyph);
  assert.equal(new Set(glyphs).size, 11, glyphs.join(" "));
});

test("an energy icon is named with the type name the page shows", () => {
  assert.equal(energyIcon("Fire").name, "Fire energy");
  assert.equal(energyIcon("Metal").name, "Steel energy");
  assert.equal(energyIcon("Colorless").name, "Normal energy");
  assert.equal(energyIcon("Fire").className, "energy-icon energy-fire");
});

test("an energy type the page does not know still renders a labelled icon", () => {
  const icon = energyIcon("Plasma");
  assert.equal(icon.glyph, "?");
  assert.equal(icon.name, "Plasma energy");
  assert.equal(icon.className, "energy-icon");
  const blank = energyIcon("");
  assert.equal(blank.glyph, "?");
  assert.equal(blank.name, "Unknown energy");
});

test("a rate limit or a timeout is not a verdict on the card, so the hash stays", () => {
  for (const status of [408, 429]) {
    assert.deepStrictEqual(deepLinkFailure({ id: "base1-4", status, found: false, networkError: false }), {
      message: "PokéSearch could not load that card. Reload the page to try again.",
      clearHash: false,
    }, String(status));
  }
});
