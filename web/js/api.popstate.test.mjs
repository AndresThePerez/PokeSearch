import test from "node:test";
import assert from "node:assert/strict";
import { popstateRunsSearch, noteHistoryNavigation, scheduleSearch, cancelScheduledSearch } from "./api.js";

// popstateRunsSearch: whether Back or Forward has to search

test("the page's own step back never searches: that search is already running", () => {
  for (const stateChanged of [false, true]) {
    for (const cancelledSearch of [false, true]) {
      assert.equal(popstateRunsSearch({ ownStepBack: true, stateChanged, cancelledSearch }), false, `${stateChanged} ${cancelledSearch}`);
    }
  }
});

test("Back to an entry with a different search runs it", () => {
  assert.equal(popstateRunsSearch({ ownStepBack: false, stateChanged: true, cancelledSearch: false }), true);
});

test("Back to the same search with nothing cancelled runs nothing", () => {
  assert.equal(popstateRunsSearch({ ownStepBack: false, stateChanged: false, cancelledSearch: false }), false);
});

test("Back that cancelled a pending keystroke search runs the search even when the address already matches", () => {
  // The grid still shows the text the keystroke abandoned.
  assert.equal(popstateRunsSearch({ ownStepBack: false, stateChanged: false, cancelledSearch: true }), true);
});

// noteHistoryNavigation reports what it cancelled

test("noteHistoryNavigation reports a pending keystroke search it cancelled", () => {
  scheduleSearch();
  assert.equal(noteHistoryNavigation(), true);
  assert.equal(noteHistoryNavigation(), false, "a second call has nothing left to cancel");
});

test("noteHistoryNavigation reports nothing when no keystroke search is pending", () => {
  scheduleSearch();
  cancelScheduledSearch();
  assert.equal(noteHistoryNavigation(), false);
});
