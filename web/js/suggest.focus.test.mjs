import test from "node:test";
import assert from "node:assert/strict";
import { enterFocusTarget, blurCloses } from "./suggest.js";

// enterFocusTarget: an answer moves focus only for the Enter that asked

test("an answer to an Enter a later Enter replaced moves nothing", () => {
  for (const outcome of ["results", "none"]) {
    assert.equal(enterFocusTarget({ outcome, inBox: true, superseded: true }), null, outcome);
  }
});

test("the latest Enter's answer still moves focus", () => {
  assert.equal(enterFocusTarget({ outcome: "results", inBox: true, superseded: false }), "results-grid");
  assert.equal(enterFocusTarget({ outcome: "none", inBox: true, superseded: false, typedSinceEnter: false }), "empty-message");
});

test("typing since Enter wins over everything else the answer says", () => {
  for (const superseded of [false, true]) {
    for (const outcome of ["results", "none", "refused", "failed", "aborted"]) {
      assert.equal(enterFocusTarget({ outcome, inBox: true, typedSinceEnter: true, superseded }), null, `${outcome} ${superseded}`);
    }
  }
});

// blurCloses: the list's delayed close after the box loses focus

test("the delayed close after a blur runs when the box is still without focus", () => {
  assert.equal(blurCloses({ focused: false }), true);
});

test("the delayed close after a blur does nothing when the box has focus again", () => {
  assert.equal(blurCloses({ focused: true }), false);
});
