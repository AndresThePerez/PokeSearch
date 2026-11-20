import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  enterEffects,
  suggestAnswerRenders,
  composingKey,
  enterFocusTarget,
  shownOutcome,
} from "./suggest.js";

// The page's markup with its comments taken out, for the ids the focus step names.
const page = readFileSync(new URL("../index.html", import.meta.url), "utf8").replace(/<!--[\s\S]*?-->/g, "");

function tagWithID(id) {
  return page.match(new RegExp(`<[a-z0-9]+\\b[^>]*\\bid="${id}"[^>]*>`))?.[0] ?? null;
}

// suggestAnswerRenders

test("a suggest answer renders only when every one of its three conditions holds", () => {
  const closes = [0, 1, 7];
  const texts = ["", "pika", "pika "];
  for (const focused of [true, false]) {
    for (const requestedAt of closes) {
      for (const closed of closes) {
        for (const requestedText of texts) {
          for (const currentText of texts) {
            const want = focused && requestedAt === closed && requestedText === currentText;
            const got = suggestAnswerRenders({ focused, requestedAt, closes: closed, requestedText, currentText });
            assert.equal(got, want, JSON.stringify({ focused, requestedAt, closed, requestedText, currentText }));
            assert.equal(typeof got, "boolean");
          }
        }
      }
    }
  }
});

test("an answer never renders without focus, however the other fields read", () => {
  for (const focused of [false, 0, "", null, undefined]) {
    assert.equal(suggestAnswerRenders({ focused, requestedAt: 2, closes: 2, requestedText: "pika", currentText: "pika" }), false, String(focused));
  }
  assert.equal(suggestAnswerRenders({}), false, "nothing known renders nothing");
});

test("a count from the future, or a text that differs in case or spacing, renders nothing", () => {
  const ordinary = { focused: true, requestedAt: 2, closes: 2, requestedText: "pika", currentText: "pika" };
  assert.equal(suggestAnswerRenders(ordinary), true);
  assert.equal(suggestAnswerRenders({ ...ordinary, requestedAt: 3 }), false, "asked after the latest close cannot be matched");
  assert.equal(suggestAnswerRenders({ ...ordinary, currentText: "Pika" }), false);
  assert.equal(suggestAnswerRenders({ ...ordinary, currentText: " pika" }), false);
  assert.equal(suggestAnswerRenders({ ...ordinary, requestedAt: "2" }), false, "a count is a number");
});

// composingKey

test("an ordinary key press is never taken for a composition", () => {
  for (const key of ["Enter", "ArrowDown", "ArrowUp", "Escape", "a", "Tab"]) {
    for (const isComposing of [false, undefined, null, 0, ""]) {
      for (const keyCode of [13, 40, 38, 27, 65, 9, 0, undefined, null, "229", 228, 230]) {
        assert.equal(composingKey({ key, isComposing, keyCode }), false, `${key} ${isComposing} ${keyCode}`);
      }
    }
  }
  assert.equal(composingKey({}), false, "an event that says nothing");
});

test("a key an input method owns is a composition however it is reported", () => {
  assert.equal(composingKey({ key: "Enter", isComposing: true, keyCode: 13 }), true);
  assert.equal(composingKey({ key: "Enter", isComposing: false, keyCode: 229 }), true, "the confirming Enter some browsers send after the composition ended");
  assert.equal(composingKey({ key: "Process", isComposing: true, keyCode: 229 }), true);
  assert.equal(composingKey({ key: "Escape", isComposing: true }), true);
  assert.equal(composingKey({ key: "ArrowUp", keyCode: 229 }), true);
});

// enterEffects while composing

test("Enter during a composition does nothing, whatever else is true, and is a new array each time", () => {
  for (const composing of [true, 1, "yes"]) {
    for (const highlighted of [false, true]) {
      for (const pending of [false, true]) {
        for (const changed of [false, true]) {
          const effects = enterEffects({ highlighted, pending, changed, composing });
          assert.deepStrictEqual(effects, [], `${highlighted} ${pending} ${changed}`);
          effects.push("poison");
          assert.deepStrictEqual(enterEffects({ highlighted, pending, changed, composing }), []);
        }
      }
    }
  }
});

test("Enter that is not composing behaves as it always did, with composing missing or falsy", () => {
  for (const composing of [undefined, false, 0, "", null]) {
    assert.deepStrictEqual(enterEffects({ highlighted: true, pending: true, changed: true, composing }), ["pick"]);
    assert.deepStrictEqual(enterEffects({ highlighted: false, pending: true, changed: false, composing }), ["close", "drop", "commit", "focus"]);
    assert.deepStrictEqual(enterEffects({ highlighted: false, pending: false, changed: true, composing }), ["close", "commit", "focus"]);
  }
});

// enterFocusTarget and shownOutcome

test("focus after Enter goes only to an element that exists and takes script focus but is no tab stop", () => {
  const outcomes = ["results", "none", "refused", "failed", "aborted", "Results", "NONE", " results", "result", "", "undefined", "null", undefined, null, 0, 1, {}, []];
  for (const inBox of [true, false, undefined, null, 0, ""]) {
    for (const outcome of outcomes) {
      const target = enterFocusTarget({ outcome, inBox });
      if (target === null) continue;
      const tag = tagWithID(target);
      assert.ok(tag, `${String(outcome)}: no element has the id ${target}`);
      assert.equal(tag.match(/\stabindex="([^"]*)"/)?.[1], "-1", `${target} must be focusable from script only`);
    }
  }
});

test("an outcome the page does not know, in any case or shape, moves nothing", () => {
  for (const outcome of ["Results", "RESULTS", "None", " results", "results ", "result", "", "undefined", undefined, null, 0, 1, true, {}, []]) {
    assert.equal(enterFocusTarget({ outcome, inBox: true }), null, String(outcome));
  }
});

test("focus moves only when the box still has it, and a box that is not known to have it is left alone", () => {
  for (const inBox of [false, undefined, null, 0, ""]) {
    for (const outcome of ["results", "none"]) {
      assert.equal(enterFocusTarget({ outcome, inBox }), null, `${String(inBox)} ${outcome}`);
    }
  }
  assert.equal(enterFocusTarget({ outcome: "results", inBox: true }), "results-grid");
  assert.equal(enterFocusTarget({ outcome: "none", inBox: true }), "empty-message");
});

test("what an Enter that ran no search finds on screen: cards first, then the empty state, else a failure", () => {
  assert.equal(shownOutcome({ gridHidden: false, emptyHidden: false }), "results", "cards win if both somehow show");
  assert.equal(shownOutcome({ gridHidden: false, emptyHidden: true }), "results");
  assert.equal(shownOutcome({ gridHidden: true, emptyHidden: false }), "none");
  assert.equal(shownOutcome({ gridHidden: true, emptyHidden: true }), "failed");
  // Whatever it reads, the answer is one enterFocusTarget knows how to place.
  for (const gridHidden of [true, false]) {
    for (const emptyHidden of [true, false]) {
      const outcome = shownOutcome({ gridHidden, emptyHidden });
      assert.ok(["results", "none", "failed"].includes(outcome));
    }
  }
});

test("the empty-state message, where focus lands when nothing was found, sits inside the empty state", () => {
  const tag = tagWithID("empty-message");
  assert.ok(tag.startsWith("<p "), tag);
  const state = page.slice(page.indexOf('id="empty-state"'));
  const closing = state.indexOf("</div>");
  assert.ok(state.slice(0, closing).includes('id="empty-message"'), "the message is part of the block that is hidden when there are results");
});
