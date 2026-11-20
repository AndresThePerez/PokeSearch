import test from "node:test";
import assert from "node:assert/strict";
import { holoTier } from "./holo.js";

test("holoTier grades plain holographic rarities as foil", () => {
  assert.equal(holoTier("Rare Holo"), "foil");
  assert.equal(holoTier("Rare Holo EX"), "foil");
  assert.equal(holoTier("Rare Ultra"), "foil");
  assert.equal(holoTier("Amazing Rare"), "foil");
  assert.equal(holoTier("Rare BREAK"), "foil");
  assert.equal(holoTier("Rare Prime"), "foil");
  assert.equal(holoTier("LEGEND"), "foil");
  assert.equal(holoTier("Double Rare"), "foil");
  assert.equal(holoTier("Illustration Rare"), "foil");
  assert.equal(holoTier("Classic Collection"), "foil");
});

test("holoTier grades the loudest rarities as prismatic", () => {
  assert.equal(holoTier("Hyper Rare"), "prismatic");
  assert.equal(holoTier("Rare Secret"), "prismatic");
  assert.equal(holoTier("Rare Rainbow"), "prismatic");
  assert.equal(holoTier("Rare Prism Star"), "prismatic");
  assert.equal(holoTier("Shiny Rare"), "prismatic");
  assert.equal(holoTier("Rare Shiny GX"), "prismatic");
  assert.equal(holoTier("Mega Hyper Rare"), "prismatic");
});

test("holoTier checks prismatic before foil", () => {
  // A name that carries a keyword from both families takes the louder tier.
  assert.equal(holoTier("Shiny Holo Rare"), "prismatic");
  assert.equal(holoTier("Rare Holo Rainbow"), "prismatic");
});

test("holoTier leaves ordinary rarities at sheen", () => {
  assert.equal(holoTier("Common"), "sheen");
  assert.equal(holoTier("Uncommon"), "sheen");
  assert.equal(holoTier("Rare"), "sheen");
  assert.equal(holoTier("Promo"), "sheen");
});

test("holoTier matches whole names for ACE SPEC and does not foil a bare ace", () => {
  assert.equal(holoTier("ACE SPEC Rare"), "foil");
  assert.equal(holoTier("Rare ACE"), "foil");
  assert.equal(holoTier("Palace Rare"), "sheen");
});

test("holoTier is case-insensitive", () => {
  assert.equal(holoTier("RARE HOLO"), "foil");
  assert.equal(holoTier("hyper rare"), "prismatic");
});

test("holoTier falls back to sheen for empty, missing and unknown input", () => {
  assert.equal(holoTier(), "sheen");
  assert.equal(holoTier(""), "sheen");
  assert.equal(holoTier(undefined), "sheen");
  assert.equal(holoTier("A Rarity Nobody Has Invented Yet"), "sheen");
});

test("holoTier coerces a non-string to text instead of throwing", () => {
  assert.equal(holoTier(null), "sheen");
  assert.equal(holoTier(42), "sheen");
});
