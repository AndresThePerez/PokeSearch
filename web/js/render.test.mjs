import test from "node:test";
import assert from "node:assert/strict";
import { imageURL, displayType, branchLabel } from "./render.js";

const LEGACY = "https://images.pokemontcg.io/ex10/1.png";

test("imageURL rewrites a legacy art URL to the scrydex URL for the size asked", () => {
  const card = { id: "base1-4", image_small: LEGACY, image_large: LEGACY };
  assert.equal(imageURL(card, "small"), "https://images.scrydex.com/pokemon/base1-4/small");
  assert.equal(imageURL(card, "large"), "https://images.scrydex.com/pokemon/base1-4/large");
});

test("imageURL percent-encodes the card id", () => {
  assert.equal(
    imageURL({ id: "ex10-?", image_small: LEGACY }, "small"),
    "https://images.scrydex.com/pokemon/ex10-%3F/small",
  );
  assert.equal(
    imageURL({ id: "a b/c#d", image_small: LEGACY }, "small"),
    "https://images.scrydex.com/pokemon/a%20b%2Fc%23d/small",
  );
  // An underscore and a hyphen are unreserved and stay as they are.
  assert.equal(
    imageURL({ id: "cel25c-17_A", image_small: LEGACY }, "small"),
    "https://images.scrydex.com/pokemon/cel25c-17_A/small",
  );
});

test("imageURL passes any other URL through unchanged", () => {
  const scrydex = "https://images.scrydex.com/pokemon/base1-4/small";
  assert.equal(imageURL({ id: "base1-4", image_small: scrydex }, "small"), scrydex);
  const other = "https://example.com/art.png";
  assert.equal(imageURL({ id: "base1-4", image_small: other }, "small"), other);
});

test("imageURL only matches the legacy host at the start of the URL", () => {
  const sneaky = "https://example.com/?u=https://images.pokemontcg.io/x.png";
  assert.equal(imageURL({ id: "base1-4", image_small: sneaky }, "small"), sneaky);
  const plain = "http://images.pokemontcg.io/x.png";
  assert.equal(imageURL({ id: "base1-4", image_small: plain }, "small"), plain);
});

test("imageURL reads the field of the size asked, so a missing size is undefined", () => {
  const card = { id: "base1-4", image_small: LEGACY };
  assert.equal(imageURL(card, "large"), undefined);
  assert.equal(imageURL({ id: "base1-4" }, "small"), undefined);
  assert.equal(imageURL({ id: "base1-4", image_small: "" }, "small"), "");
});

test("displayType renames the two internal type names", () => {
  assert.equal(displayType("Metal"), "Steel");
  assert.equal(displayType("Colorless"), "Normal");
});

test("displayType leaves every other value alone", () => {
  for (const t of ["Fire", "Water", "Grass", "Lightning", "Psychic", "Fighting", "Darkness", "Dragon", "Fairy", "Steel", "Normal", "", "metal"]) {
    assert.equal(displayType(t), t);
  }
  assert.equal(displayType(undefined), undefined);
});

test("branchLabel shortens the fuzzy name branch and keeps the others", () => {
  assert.equal(branchLabel("fuzzy-name"), "fuzzy");
  assert.equal(branchLabel("exact"), "exact");
  assert.equal(branchLabel("prefix"), "prefix");
  assert.equal(branchLabel("text"), "text");
});

test("branchLabel shows a name the page does not know as it is, including names an object would inherit", () => {
  assert.equal(branchLabel("phonetic"), "phonetic");
  assert.equal(branchLabel(""), "");
  // A branch named like an object key is still a name the page does not
  // know, so it is shown as it is, never as what the prototype holds.
  assert.equal(branchLabel("constructor"), "constructor");
});
