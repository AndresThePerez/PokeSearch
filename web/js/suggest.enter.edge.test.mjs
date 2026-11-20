import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { enterEffects } from "./suggest.js";

// enterEffects over every input the keydown handler can hand it, and the
// wiring of the search box's keydown listener.

const CANONICAL = {
  pick: ["pick"],
  settled: ["close", "drop", "commit", "focus"],
  changed: ["close", "commit", "focus"],
};

function expected({ highlighted, pending, changed }) {
  if (highlighted) return CANONICAL.pick;
  if (!changed && pending) return CANONICAL.settled;
  return CANONICAL.changed;
}

test("enterEffects follows its table for all eight inputs, and reads truthy and falsy values by their truth", () => {
  const values = [false, true, 0, 1, "", "yes", null, undefined];
  for (const highlighted of values) {
    for (const pending of values) {
      for (const changed of values) {
        const got = enterEffects({ highlighted, pending, changed });
        assert.deepStrictEqual(got, expected({ highlighted, pending, changed }), `${highlighted} ${pending} ${changed}`);
      }
    }
  }
  assert.deepStrictEqual(enterEffects({}), CANONICAL.changed, "nothing known: close, commit, move to the results");
});

test("whatever Enter decides, a pick is never a commit, focus never goes with a pick, and nothing repeats", () => {
  for (const highlighted of [false, true]) {
    for (const pending of [false, true]) {
      for (const changed of [false, true]) {
        const effects = enterEffects({ highlighted, pending, changed });
        const label = `${highlighted} ${pending} ${changed}`;
        assert.ok(!(effects.includes("pick") && effects.includes("commit")), label);
        assert.ok(!(effects.includes("pick") && effects.includes("focus")), label);
        assert.equal(new Set(effects).size, effects.length, `${label}: no effect twice`);
        if (effects.includes("commit")) {
          assert.ok(effects.indexOf("close") < effects.indexOf("commit"), `${label}: close precedes commit`);
        }
        if (effects.includes("drop")) {
          assert.ok(effects.indexOf("drop") < effects.indexOf("commit"), `${label}: a waiting search is dropped before the commit looks at it`);
        }
        assert.ok(effects.every((effect) => ["pick", "close", "drop", "commit", "focus"].includes(effect)), label);
      }
    }
  }
});

test("each call returns a fresh array: changing one result cannot change the next", () => {
  const inputs = [
    { highlighted: true, pending: false, changed: false },
    { highlighted: false, pending: true, changed: false },
    { highlighted: false, pending: false, changed: true },
  ];
  for (const input of inputs) {
    const first = enterEffects(input);
    first.length = 0;
    first.push("poison");
    Object.freeze(enterEffects(input));
    assert.deepStrictEqual(enterEffects(input), expected(input), JSON.stringify(input));
  }
});

test("the search box has one keydown listener, and it is the suggest module's", () => {
  // Two listeners would each act on Enter: a second search, a second focus move.
  const dir = new URL(".", import.meta.url);
  let listeners = 0;
  for (const name of readdirSync(dir).filter((file) => file.endsWith(".js"))) {
    const source = readFileSync(new URL(name, dir), "utf8");
    for (const match of source.matchAll(/\$\("search-input"\)\s*\.addEventListener\(\s*"keydown"\s*,\s*([^)]*?)\)?\s*;/g)) {
      listeners += 1;
      assert.match(match[1], /^handleSuggestKeydown\b/, `${name} binds something else to the search box's keydown`);
    }
    assert.ok(!/search-input[^;\n]*\.onkeydown/.test(source), `${name} sets onkeydown on the search box`);
  }
  assert.equal(listeners, 1);
});
