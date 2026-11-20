import test from "node:test";
import assert from "node:assert/strict";
import { holoTier } from "./holo.js";

// Every rarity the corpus holds, with the tier the keyword rules give it. A
// change to the keyword lists that moves any of these has to move it here too.
const CORPUS = {
  Common: "sheen",
  Uncommon: "sheen",
  Rare: "sheen",
  Promo: "sheen",
  "Pikachu Rare": "sheen",
  "Futuristic Rare": "sheen",
  "Rare Holo": "foil",
  "Rare Ultra": "foil",
  "Ultra Rare": "foil",
  "Illustration Rare": "foil",
  "Double Rare": "foil",
  "Rare Holo EX": "foil",
  "Rare Holo ex": "foil",
  "Rare Holo V": "foil",
  "Rare Holo GX": "foil",
  "Rare Holo VMAX": "foil",
  "Rare Holo VSTAR": "foil",
  "Rare Holo LV.X": "foil",
  "Rare Holo Star": "foil",
  "Holo Rare V": "foil",
  "Holo Rare VMAX": "foil",
  "Holo Rare VSTAR": "foil",
  "Trainer Gallery Rare Holo": "foil",
  "Special Illustration Rare": "foil",
  "ACE SPEC Rare": "foil",
  "Rare ACE": "foil",
  "Rare BREAK": "foil",
  "Rare Prime": "foil",
  "Classic Collection": "foil",
  LEGEND: "foil",
  "Rare Shining": "foil",
  "Radiant Rare": "foil",
  "Amazing Rare": "foil",
  "Black White Rare": "foil",
  "Rare Secret": "prismatic",
  "Rare Rainbow": "prismatic",
  "Rare Shiny": "prismatic",
  "Shiny Rare": "prismatic",
  "Rare Shiny GX": "prismatic",
  "Shiny Ultra Rare": "prismatic",
  "Hyper Rare": "prismatic",
  "Rare Prism Star": "prismatic",
  "Mega Hyper Rare": "prismatic",
  MEGA_ATTACK_RARE: "prismatic",
};

test("holoTier grades every rarity in the corpus", () => {
  assert.equal(Object.keys(CORPUS).length, 44);
  for (const [rarity, tier] of Object.entries(CORPUS)) {
    assert.equal(holoTier(rarity), tier, rarity);
  }
});

test("holoTier gives the same tier to a rarity whatever its case", () => {
  for (const rarity of Object.keys(CORPUS)) {
    const tier = holoTier(rarity);
    assert.equal(holoTier(rarity.toUpperCase()), tier, rarity);
    assert.equal(holoTier(rarity.toLowerCase()), tier, rarity);
  }
  assert.equal(holoTier("Rare Holo EX"), holoTier("Rare Holo ex"));
});

test("holoTier ignores surrounding space and never returns anything but the three tiers", () => {
  const tiers = new Set(["sheen", "foil", "prismatic"]);
  const odd = ["", " ", "\t", "\n", "  Rare Holo  ", "rare holo", "Rare​Holo", "Ünicode Rare", "💥", "a".repeat(100000), "holo".repeat(10000)];
  for (const r of odd) assert.ok(tiers.has(holoTier(r)), JSON.stringify(r.slice(0, 20)));
  assert.equal(holoTier("  Rare Holo  "), "foil");
  // A space that is not a space is not the word break two-word keys need.
  assert.equal(holoTier("Double Rare"), "sheen");
});

test("holoTier takes the loudest tier when a name carries keywords of both", () => {
  assert.equal(holoTier("Rare Holo Secret"), "prismatic");
  assert.equal(holoTier("Mega Rare Holo"), "prismatic");
  assert.equal(holoTier("Holo Crown"), "prismatic");
});
