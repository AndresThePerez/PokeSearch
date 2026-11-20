import test from "node:test";
import assert from "node:assert/strict";
import { imageURL, displayType, branchLabel } from "./render.js";

const LEGACY = "https://images.pokemontcg.io/x/1.png";

// A card id is data from the API and goes into a URL path. Whatever it holds,
// the result must stay one path segment on the one host the page trusts, and
// decode back to the id.
test("imageURL keeps any card id inside a single path segment on the image host", () => {
  const ids = ["a/b", "a#b", "a?b=1", "a%b", "a%2Fb", "a b", "../x", "é", "ex10-?", "a&b=c", "a\\b", "\u{1F600}", "a\nb", "a;b", "a+b"];
  for (const id of ids) {
    const url = imageURL({ id, image_small: LEGACY }, "small");
    const parsed = new URL(url);
    assert.equal(parsed.host, "images.scrydex.com", id);
    assert.equal(parsed.search, "", id);
    assert.equal(parsed.hash, "", id);
    const segments = parsed.pathname.split("/");
    assert.equal(segments.length, 4, `${id} -> ${url}`);
    assert.equal(segments[1], "pokemon");
    assert.equal(segments[3], "small");
    assert.equal(decodeURIComponent(segments[2]), id);
  }
});

test("imageURL rewrites only an exact legacy host, not a lookalike or another scheme or case", () => {
  const same = [
    "https://images.pokemontcg.io.example/a.png",
    "https://images.pokemontcg.io@example.com/a.png",
    "http://images.pokemontcg.io/a.png",
    "HTTPS://images.pokemontcg.io/a.png",
    "https://IMAGES.pokemontcg.io/a.png",
    "https://images.pokemontcg.io",
    " https://images.pokemontcg.io/a.png",
    "",
  ];
  for (const source of same) {
    assert.equal(imageURL({ id: "base1-4", image_small: source }, "small"), source, JSON.stringify(source));
  }
});

// The caller hands the result to an <img> as is, so "nothing" has to stay
// nothing (null or undefined) and never become a made-up address.
test("imageURL gives nothing, and does not throw, when the card has no art for that size", () => {
  const empty = (value) => value === undefined || value === null;
  assert.ok(empty(imageURL({ id: "base1-4" }, "small")));
  assert.ok(empty(imageURL({ id: "base1-4", image_small: null }, "small")));
  assert.ok(empty(imageURL({ id: "base1-4", image_small: LEGACY }, "large")));
  assert.ok(empty(imageURL({ id: "base1-4", image_small: LEGACY }, "medium")));
});

test("imageURL picks the size asked for, not a neighbouring field", () => {
  const card = { id: "base1-4", image_small: LEGACY, image_large: "https://example.com/big.png" };
  assert.equal(imageURL(card, "large"), "https://example.com/big.png");
  assert.equal(imageURL(card, "small"), "https://images.scrydex.com/pokemon/base1-4/small");
});

test("displayType renames only the two exact legacy names", () => {
  for (const [given, shown] of [
    ["Metal", "Steel"], ["Colorless", "Normal"],
    ["metal", "metal"], ["METAL", "METAL"], ["Metal ", "Metal "], ["colorless", "colorless"],
    ["Steel", "Steel"], ["Normal", "Normal"], ["Fire", "Fire"], ["", ""],
  ]) {
    assert.equal(displayType(given), shown, JSON.stringify(given));
  }
  assert.equal(displayType(undefined), undefined);
  assert.equal(displayType(null), null);
});

test("branchLabel shortens only fuzzy-name and shows every other branch under its own name", () => {
  assert.equal(branchLabel("exact"), "exact");
  assert.equal(branchLabel("prefix"), "prefix");
  assert.equal(branchLabel("text"), "text");
  assert.equal(branchLabel("fuzzy-name"), "fuzzy");
  for (const name of ["fuzzy", "Exact", "fuzzy-name ", "", "phonetic", "new-branch"]) {
    assert.equal(branchLabel(name), name, JSON.stringify(name));
  }
});
