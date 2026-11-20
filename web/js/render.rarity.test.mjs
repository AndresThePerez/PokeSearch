import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { RARITY_LABELS, rarityLabel, facetOption, activeFilters } from "./render.js";
import { readStateFromURL, buildParams } from "./state.js";

// The archive's 44 rarity values, spelled as the index stores them.
const STORED = [
  "ACE SPEC Rare", "Amazing Rare", "Black White Rare", "Classic Collection",
  "Common", "Double Rare", "Futuristic Rare", "Holo Rare V",
  "Holo Rare VMAX", "Holo Rare VSTAR", "Hyper Rare", "Illustration Rare",
  "LEGEND", "MEGA_ATTACK_RARE", "Mega Hyper Rare", "Pikachu Rare",
  "Promo", "Radiant Rare", "Rare", "Rare ACE",
  "Rare BREAK", "Rare Holo", "Rare Holo EX", "Rare Holo GX",
  "Rare Holo LV.X", "Rare Holo Star", "Rare Holo V", "Rare Holo VMAX",
  "Rare Holo VSTAR", "Rare Holo ex", "Rare Prime", "Rare Prism Star",
  "Rare Rainbow", "Rare Secret", "Rare Shining", "Rare Shiny",
  "Rare Shiny GX", "Rare Ultra", "Shiny Rare", "Shiny Ultra Rare",
  "Special Illustration Rare", "Trainer Gallery Rare Holo", "Ultra Rare", "Uncommon",
];

// rarityLabel: what the page shows for a rarity value

test("a rarity that reads badly raw is shown with a label", () => {
  assert.equal(rarityLabel("MEGA_ATTACK_RARE"), "Mega Attack Rare");
  assert.equal(rarityLabel("LEGEND"), "Legend");
});

test("a rarity with no label is shown as it is", () => {
  for (const value of ["Rare Holo", "Rare Holo EX", "Rare Holo ex", "ACE SPEC Rare", "Rare BREAK", "Bogus", "rare holo", ""]) {
    assert.equal(rarityLabel(value), value);
  }
});

test("a rarity named like an object key is shown as it is", () => {
  for (const value of ["constructor", "__proto__", "toString", "hasOwnProperty", "valueOf"]) {
    assert.equal(rarityLabel(value), value);
  }
});

test("the label map is small, names only stored values, and never merges or renames one into another", () => {
  const keys = Object.keys(RARITY_LABELS);
  assert.ok(keys.length > 0 && keys.length <= 4, `${keys.length} labels`);
  for (const key of keys) assert.ok(STORED.includes(key), `${key} is a stored value`);
  const shown = STORED.map(rarityLabel);
  assert.equal(new Set(shown).size, STORED.length, "every stored value keeps a label of its own");
  for (const [value, label] of Object.entries(RARITY_LABELS)) {
    assert.ok(!STORED.includes(label), `${label} is not another stored value`);
    assert.notEqual(label, value);
  }
  assert.ok(Object.isFrozen(RARITY_LABELS));
});

// facetOption: a filter option shows the label and sends the raw value

test("a rarity option reads its label and its count, and its value is the stored value", () => {
  assert.deepStrictEqual(facetOption({ value: "MEGA_ATTACK_RARE", count: 7 }, rarityLabel), { text: "Mega Attack Rare (7)", value: "MEGA_ATTACK_RARE" });
  assert.deepStrictEqual(facetOption({ value: "Rare Holo", count: 1621 }, rarityLabel), { text: `Rare Holo (${(1621).toLocaleString()})`, value: "Rare Holo" });
});

test("no option ever sends a label as a filter value", () => {
  for (const value of STORED) {
    const option = facetOption({ value, count: 1 }, rarityLabel);
    assert.equal(option.value, value);
  }
  for (const label of Object.values(RARITY_LABELS)) {
    assert.ok(!STORED.map((value) => facetOption({ value, count: 1 }, rarityLabel).value).includes(label), label);
  }
});

test("an option with no label function shows its value", () => {
  assert.deepStrictEqual(facetOption({ value: "Sword & Shield", count: 3667 }), { text: `Sword & Shield (${(3667).toLocaleString()})`, value: "Sword & Shield" });
});

test("a selected rarity is still requested and addressed by its stored value", () => {
  globalThis.location = { search: "?rarity=MEGA_ATTACK_RARE", pathname: "/", hash: "" };
  readStateFromURL();
  assert.equal(buildParams().toString(), "rarity=MEGA_ATTACK_RARE");
  assert.equal(buildParams({ page: true, debug: true }).get("rarity"), "MEGA_ATTACK_RARE");
  globalThis.location = { search: "", pathname: "/", hash: "" };
  readStateFromURL();
});

// activeFilters: the chips, with their labels and the values they remove

test("the rarity chip reads the label and holds the stored value", () => {
  const chips = activeFilters({ supertype: "", types: [], set: "", rarity: "MEGA_ATTACK_RARE", series: "" });
  assert.deepStrictEqual(chips, [{ field: "rarity", value: "MEGA_ATTACK_RARE", label: "Mega Attack Rare" }]);
});

test("chips come in the rail's order with the names the page uses", () => {
  const from = { supertype: "pokemon", types: ["Metal", "Fire", "Colorless"], set: "base1", rarity: "LEGEND", series: "Base" };
  const chips = activeFilters(from, (id) => (id === "base1" ? "Base" : id));
  assert.deepStrictEqual(chips.map(({ field, value, label }) => `${field}:${value}:${label}`), [
    "supertype:pokemon:Pokémon",
    "types:Metal:Steel",
    "types:Fire:Fire",
    "types:Colorless:Normal",
    "set:base1:Base",
    "rarity:LEGEND:Legend",
    "series:Base:Base",
  ]);
  assert.deepStrictEqual(from.types, ["Metal", "Fire", "Colorless"], "the input is left alone");
});

test("a set with no known name is shown by its id, and no filter means no chip", () => {
  assert.deepStrictEqual(activeFilters({ supertype: "", types: [], set: "zz9", rarity: "", series: "" }), [{ field: "set", value: "zz9", label: "zz9" }]);
  assert.deepStrictEqual(activeFilters({ supertype: "", types: [], set: "", rarity: "", series: "" }), []);
  assert.equal(activeFilters({ supertype: "trainer", types: [], set: "", rarity: "", series: "" })[0].label, "Trainer");
});

// The card dialog and the Stats view use the same label.

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const strip = (source) => source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");

test("the card dialog shows a card's rarity by its label", () => {
  const modal = strip(read("./modal.js"));
  assert.match(modal, /import \{[^}]*\brarityLabel\b[^}]*\} from "\.\/render\.js"/);
  assert.match(modal, /const meta = \[rarityLabel\(card\.rarity\)/);
});

test("the Stats view lists rarities by their labels", () => {
  const stats = strip(read("./stats.js"));
  assert.match(stats, /import \{[^}]*\brarityLabel\b[^}]*\} from "\.\/render\.js"/);
  assert.match(stats, /barRows\(head, \(bucket\) => rarityLabel\(bucket\.value\)\)/);
});

test("the rarity select is filled with labelled options", () => {
  const render = strip(read("./render.js"));
  assert.match(render, /populateFacetSelect\(\$\("rarity-select"\), "All rarities", facets\.rarity, state\.rarity, rarityLabel\)/);
});
