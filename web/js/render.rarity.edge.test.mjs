import test from "node:test";
import assert from "node:assert/strict";
import { RARITY_LABELS, rarityLabel, facetOption, activeFilters } from "./render.js";
import { acceptedFilters } from "./state.js";

// A rarity is only ever shown under a label: what the filter sends, what the
// address holds and what a chip removes stay the stored value.

const STORED = [
  "ACE SPEC Rare", "Amazing Rare", "Black White Rare", "Classic Collection", "Common", "Double Rare",
  "Futuristic Rare", "Holo Rare V", "Holo Rare VMAX", "Holo Rare VSTAR", "Hyper Rare", "Illustration Rare",
  "LEGEND", "MEGA_ATTACK_RARE", "Mega Hyper Rare", "Pikachu Rare", "Promo", "Radiant Rare", "Rare", "Rare ACE",
  "Rare BREAK", "Rare Holo", "Rare Holo EX", "Rare Holo GX", "Rare Holo LV.X", "Rare Holo Star", "Rare Holo V",
  "Rare Holo VMAX", "Rare Holo VSTAR", "Rare Holo ex", "Rare Prime", "Rare Prism Star", "Rare Rainbow",
  "Rare Secret", "Rare Shining", "Rare Shiny", "Rare Shiny GX", "Rare Ultra", "Shiny Rare", "Shiny Ultra Rare",
  "Special Illustration Rare", "Trainer Gallery Rare Holo", "Ultra Rare", "Uncommon",
];

const bare = { supertype: "", types: [], set: "", rarity: "", series: "" };

test("the forty-four option texts are all different, and no label makes two read alike that did not before", () => {
  const texts = STORED.map((value, at) => facetOption({ value, count: at + 1 }, rarityLabel).text);
  assert.equal(new Set(texts).size, 44);
  // Rare Holo EX and Rare Holo ex are stored apart and differ in letter case
  // alone; apart from that pair every option reads differently, labels or not.
  const fold = (text) => text.replace(/ \(\d+\)$/, "").toLowerCase().replace(/[_\s]+/g, " ");
  const withLabels = new Set(texts.map(fold));
  const raw = new Set(STORED.map((value, at) => fold(`${value} (${at + 1})`)));
  assert.equal(withLabels.size, 43);
  assert.equal(raw.size, 43);
});

test("a rarity selected at zero cards is kept as an option under its label and still sends the stored value", () => {
  for (const value of STORED) {
    const option = facetOption({ value, count: 0 }, rarityLabel);
    assert.equal(option.value, value);
    assert.equal(option.text, `${rarityLabel(value)} (0)`);
  }
});

test("the option and chip for a stored value that is also a label's key never swap value and text", () => {
  for (const [key, label] of Object.entries(RARITY_LABELS)) {
    const option = facetOption({ value: key, count: 3 }, rarityLabel);
    assert.equal(option.value, key);
    assert.ok(option.text.startsWith(label));
    const [chip] = activeFilters({ ...bare, rarity: key });
    assert.deepStrictEqual(chip, { field: "rarity", value: key, label });
  }
});

test("the label map cannot be changed by a script, and a refused change leaves the labels alone", () => {
  assert.throws(() => { RARITY_LABELS.LEGEND = "LEGEND"; }, TypeError);
  assert.throws(() => { RARITY_LABELS.Common = "Plain"; }, TypeError);
  assert.throws(() => { delete RARITY_LABELS.LEGEND; }, TypeError);
  assert.equal(rarityLabel("LEGEND"), "Legend");
  assert.equal(rarityLabel("Common"), "Common");
});

test("a rarity of an odd kind is handed back as it came and never throws", () => {
  for (const value of [undefined, null, 0, 5, NaN, true, "", " ", "legend", " LEGEND", "LEGEND ", "Legend", "mega attack rare"]) {
    assert.ok(Object.is(rarityLabel(value), value) || rarityLabel(value) === value, `${String(value)}`);
  }
});

test("the displayed label is not read back as a stored value: typing it is a rarity that matches nothing", () => {
  const facets = { rarity: STORED.map((value) => ({ value, count: 4 })) };
  for (const [key, label] of Object.entries(RARITY_LABELS)) {
    for (const spelling of [label, label.toUpperCase(), label.toLowerCase()]) {
      if (spelling.toLowerCase() === key.toLowerCase()) continue;
      const state = { ...bare, types: [], q: "", sort: "", order: "", page: 1, rarity: spelling };
      assert.equal(acceptedFilters({ state, total: 9, facets }).rarity, spelling, `${spelling} was taken for ${key}`);
    }
  }
});

test("chips hold the state's own text for every rarity, whatever the label, and the state is not touched", () => {
  for (const value of STORED) {
    const from = Object.freeze({ ...bare, types: Object.freeze([]), rarity: value });
    const chips = activeFilters(from);
    assert.equal(chips.length, 1);
    assert.equal(chips[0].value, value);
    assert.equal(chips[0].field, "rarity");
    assert.equal(chips[0].label, rarityLabel(value));
  }
});

test("the order of the chips does not depend on the order a state's fields were written in", () => {
  const a = { series: "S", rarity: "LEGEND", set: "x", types: ["Fire", "Metal"], supertype: "energy" };
  const b = { supertype: "energy", types: ["Fire", "Metal"], set: "x", rarity: "LEGEND", series: "S" };
  const order = (from) => activeFilters(from).map((chip) => chip.field);
  assert.deepStrictEqual(order(a), ["supertype", "types", "types", "set", "rarity", "series"]);
  assert.deepStrictEqual(order(a), order(b));
});

test("a set whose name cannot be found is shown by its id, however the lookup fails", () => {
  for (const lookup of [() => undefined, () => null, undefined]) {
    const [chip] = activeFilters({ ...bare, set: "base1" }, lookup);
    assert.deepStrictEqual(chip, { field: "set", value: "base1", label: "base1" });
  }
});

test("a count of zero and a count in the thousands are written as numbers, never as a word", () => {
  assert.equal(facetOption({ value: "Common", count: 0 }, rarityLabel).text, "Common (0)");
  assert.equal(facetOption({ value: "Common", count: 5407 }, rarityLabel).text, `Common (${(5407).toLocaleString()})`);
  assert.doesNotMatch(facetOption({ value: "Common", count: 12 }, rarityLabel).text, /NaN|undefined|null/);
});
