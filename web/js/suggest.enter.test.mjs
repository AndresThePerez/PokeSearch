import test from "node:test";
import assert from "node:assert/strict";
import { enterEffects } from "./suggest.js";

// enterEffects: what Enter in the search box does

test("Enter on a highlighted option picks it and does nothing else", () => {
  for (const pending of [false, true]) {
    for (const changed of [false, true]) {
      assert.deepEqual(enterEffects({ highlighted: true, pending, changed }), ["pick"], `${pending} ${changed}`);
    }
  }
});

test("Enter on a changed query closes the list, commits it once and moves to the results", () => {
  for (const pending of [false, true]) {
    const effects = enterEffects({ highlighted: false, pending, changed: true });
    assert.deepEqual(effects, ["close", "commit", "focus"], String(pending));
    assert.equal(effects.filter((effect) => effect === "commit").length, 1, "the search runs once");
    assert.ok(!effects.includes("drop"), "the waiting keystroke search is the one the commit runs");
  }
});

test("Enter on a query whose results are shown drops the waiting keystroke search instead of repeating it", () => {
  const effects = enterEffects({ highlighted: false, pending: true, changed: false });
  assert.deepEqual(effects, ["close", "drop", "commit", "focus"]);
});

test("Enter on a query whose results are shown, with nothing waiting, searches nothing new", () => {
  const effects = enterEffects({ highlighted: false, pending: false, changed: false });
  assert.deepEqual(effects, ["close", "commit", "focus"]);
});

test("Enter without a highlighted option always closes the list first and ends on the results", () => {
  for (const pending of [false, true]) {
    for (const changed of [false, true]) {
      const effects = enterEffects({ highlighted: false, pending, changed });
      assert.equal(effects[0], "close", `${pending} ${changed}`);
      assert.equal(effects.at(-1), "focus", `${pending} ${changed}`);
      assert.ok(!effects.includes("pick"), `${pending} ${changed}`);
    }
  }
});

test("Enter that confirms an input-method composition does nothing: no commit, no focus move", () => {
  for (const pending of [false, true]) {
    for (const changed of [false, true]) {
      for (const highlighted of [false, true]) {
        assert.deepEqual(enterEffects({ highlighted, pending, changed, composing: true }), [], `${highlighted} ${pending} ${changed}`);
      }
    }
  }
});
