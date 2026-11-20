import test from "node:test";
import assert from "node:assert/strict";
import { popstateRunsSearch, noteHistoryNavigation, scheduleSearch, cancelScheduledSearch } from "./api.js";

test("popstateRunsSearch follows its truth table for all eight inputs", () => {
  for (const ownStepBack of [false, true]) {
    for (const stateChanged of [false, true]) {
      for (const cancelledSearch of [false, true]) {
        const want = !ownStepBack && (stateChanged || cancelledSearch);
        const got = popstateRunsSearch({ ownStepBack, stateChanged, cancelledSearch });
        assert.equal(got, want, `${ownStepBack} ${stateChanged} ${cancelledSearch}`);
      }
    }
  }
});

test("popstateRunsSearch reads missing or non-boolean inputs by their truth, and the page's step back still wins", () => {
  const values = [undefined, null, 0, 1, "", "yes", NaN];
  for (const ownStepBack of values) {
    for (const stateChanged of values) {
      for (const cancelledSearch of values) {
        const got = popstateRunsSearch({ ownStepBack, stateChanged, cancelledSearch });
        const want = !ownStepBack && Boolean(stateChanged || cancelledSearch);
        assert.equal(Boolean(got), want, `${ownStepBack} ${stateChanged} ${cancelledSearch}`);
      }
    }
  }
  assert.equal(Boolean(popstateRunsSearch({})), false, "nothing known, nothing searched");
});

test("a burst of keystrokes is one pending search: Back reports it once, and nothing after", () => {
  for (let i = 0; i < 50; i += 1) scheduleSearch();
  assert.equal(noteHistoryNavigation(), true);
  for (let i = 0; i < 5; i += 1) {
    assert.equal(noteHistoryNavigation(), false, `call ${i + 2} has nothing left to cancel`);
  }
});

test("a pending search cancelled by a picked suggestion is not reported again by Back", () => {
  scheduleSearch();
  cancelScheduledSearch();
  cancelScheduledSearch();
  assert.equal(noteHistoryNavigation(), false);
  // And a new burst after that is reported again.
  scheduleSearch();
  assert.equal(noteHistoryNavigation(), true);
  assert.equal(noteHistoryNavigation(), false);
});
