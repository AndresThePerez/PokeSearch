import test from "node:test";
import assert from "node:assert/strict";
import { suggestAnswerRenders, composingKey, enterFocusTarget, shownOutcome } from "./suggest.js";

// suggestAnswerRenders: whether a suggest answer may open the list

const ordinary = { focused: true, requestedAt: 3, closes: 3, requestedText: "pika", currentText: "pika" };

test("an answer for the text in the focused box, with no close since it was asked, renders", () => {
  assert.equal(suggestAnswerRenders(ordinary), true);
});

test("an answer to a request made before the list was closed renders nothing", () => {
  assert.equal(suggestAnswerRenders({ ...ordinary, closes: 4 }), false);
  assert.equal(suggestAnswerRenders({ ...ordinary, requestedAt: 0, closes: 9 }), false);
});

test("an answer that arrives when the box has no focus renders nothing", () => {
  assert.equal(suggestAnswerRenders({ ...ordinary, focused: false }), false);
});

test("an answer for text the box no longer holds renders nothing", () => {
  assert.equal(suggestAnswerRenders({ ...ordinary, currentText: "pikac" }), false);
  assert.equal(suggestAnswerRenders({ ...ordinary, currentText: "" }), false);
});

// composingKey: a key that belongs to an input method, not to the page

test("a key pressed while an input method is composing belongs to the input method", () => {
  assert.equal(composingKey({ key: "Enter", isComposing: true, keyCode: 13 }), true);
  assert.equal(composingKey({ key: "Enter", isComposing: false, keyCode: 229 }), true);
  assert.equal(composingKey({ key: "ArrowDown", isComposing: true, keyCode: 40 }), true);
});

test("an ordinary key press belongs to the page", () => {
  assert.equal(composingKey({ key: "Enter", isComposing: false, keyCode: 13 }), false);
  assert.equal(composingKey({ key: "Escape", keyCode: 27 }), false);
});

// enterFocusTarget: where focus goes once the search Enter asked for answers

test("results take focus to the grid", () => {
  assert.equal(enterFocusTarget({ outcome: "results", inBox: true }), "results-grid");
});

test("no results take focus to the empty-state message, which says so", () => {
  assert.equal(enterFocusTarget({ outcome: "none", inBox: true }), "empty-message");
});

test("a search that could not run leaves focus in the box", () => {
  assert.equal(enterFocusTarget({ outcome: "refused", inBox: true }), null);
  assert.equal(enterFocusTarget({ outcome: "failed", inBox: true }), null);
});

test("a search a newer one replaced moves nothing", () => {
  assert.equal(enterFocusTarget({ outcome: "aborted", inBox: true }), null);
});

test("focus the visitor already moved elsewhere is left where it is", () => {
  for (const outcome of ["results", "none", "refused", "failed", "aborted"]) {
    assert.equal(enterFocusTarget({ outcome, inBox: false }), null, outcome);
  }
});

// shownOutcome: what an Enter that ran no search already has on screen

test("an Enter that runs no search reads the outcome from what is shown", () => {
  assert.equal(shownOutcome({ gridHidden: false, emptyHidden: true }), "results");
  assert.equal(shownOutcome({ gridHidden: true, emptyHidden: false }), "none");
  assert.equal(shownOutcome({ gridHidden: true, emptyHidden: true }), "failed");
});
