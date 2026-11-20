import test from "node:test";
import assert from "node:assert/strict";
import { labCardCode, setCodeAndNumber } from "./telemetry.js";

// labCardCode: the small line under a Ranking lab row's name.

const IDS = [
  ["base1-4", "base1 #4"],
  ["cel25c-17_A", "cel25c #17_A"],
  ["swsh12pt5gg-GG01", "swsh12pt5gg #GG01"],
  ["ex10-?", "ex10 #?"],
  ["sv3pt5-1-2", "sv3pt5-1 #2"],
  ["promo", "promo"],
  ["-17", "#17"],
  ["abc-", "abc"],
  ["-", ""],
  ["", ""],
  ["---", "--"],
  [" - ", ""],
  ["abc - 4", "abc #4"],
  ["<b>x</b>-\"1'", "<b>x</b> #\"1'"],
];

test("the code line is the set code and the number, each left out when the id has none", () => {
  for (const [id, line] of IDS) assert.equal(labCardCode(id), line, JSON.stringify(id));
});

test("the code line never starts or ends with a space, never stands as a lone #, and never names a missing value", () => {
  const ids = [
    ...IDS.map(([id]) => id),
    `${"q".repeat(300)}-${"9".repeat(300)}`,
    "x".repeat(300),
    "-".repeat(300),
    "a-#",
    "#-#",
    "\t-\n",
    "pokémon-ⅰ",
  ];
  for (const id of ids) {
    const line = labCardCode(id);
    assert.equal(typeof line, "string");
    assert.equal(line, line.trim(), JSON.stringify(id.slice(0, 20)));
    assert.notEqual(line.trim(), "#", JSON.stringify(id.slice(0, 20)));
    assert.ok(!line.endsWith(" #"), JSON.stringify(id.slice(0, 20)));
    assert.doesNotMatch(line, /undefined|null|NaN|\s{2}/, JSON.stringify(id.slice(0, 20)));
  }
});

test("an id that is not a string gives no code line and never throws", () => {
  for (const id of [undefined, null, 0, 17, Number.NaN, true, {}, [], ["base1-4"], { toString: () => "base1-4" }, Symbol("id")]) {
    let line;
    assert.doesNotThrow(() => { line = labCardCode(id); }, typeof id);
    assert.equal(line, "", typeof id);
  }
});

test("the code line and the set code and number read the same id the same way", () => {
  for (const [id] of IDS) {
    const { set, number } = setCodeAndNumber(id);
    if (set.trim() && number.trim()) assert.equal(labCardCode(id), `${set.trim()} #${number.trim()}`, id);
    if (!set.trim() && !number.trim()) assert.equal(labCardCode(id), "", id);
  }
  const long = `${"q".repeat(300)}-${"9".repeat(300)}`;
  assert.equal(labCardCode(long), `${"q".repeat(300)} #${"9".repeat(300)}`, "nothing is cut: the row's style clips it");
});
