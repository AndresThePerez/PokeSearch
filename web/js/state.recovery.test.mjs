import test from "node:test";
import assert from "node:assert/strict";
import {
  state,
  defaults,
  readStateFromURL,
  withoutField,
  failureAction,
  ignoredNotice,
  historyAction,
} from "./state.js";

function load(search = "") {
  globalThis.location = { search, pathname: "/", hash: "" };
  readStateFromURL();
}

test.beforeEach(() => load(""));
test.after(() => {
  delete globalThis.location;
});

function envelope(field, message = "bad value") {
  const error = { code: "invalid_param", message };
  if (field !== undefined) error.field = field;
  return { error, request_id: "0000000000000000" };
}

const FULL = "?q=pika&supertype=trainer&types=Fire,Water&set=base1&rarity=Rare&series=Base&sort=hp&order=asc";

// withoutField

test("withoutField resets each field the address holds to its default", () => {
  load(FULL);
  for (const field of ["q", "supertype", "types", "set", "rarity", "series", "sort", "order"]) {
    const next = withoutField(state, field);
    assert.deepStrictEqual(next[field], defaults[field], `${field} not reset`);
    for (const other of ["q", "supertype", "types", "set", "rarity", "series", "sort", "order"]) {
      if (other !== field) assert.deepStrictEqual(next[other], state[other], `${field} also changed ${other}`);
    }
  }
});

test("withoutField covers every key the state holds", () => {
  load(FULL);
  state.page = 4;
  for (const field of Object.keys(defaults)) {
    assert.deepStrictEqual(withoutField(state, field)[field], defaults[field], `${field} not reset`);
  }
});

test("withoutField never mutates its input and never shares the types list", () => {
  load(FULL);
  const before = JSON.stringify(state);
  const typesBefore = state.types;
  const next = withoutField(state, "sort");
  assert.equal(JSON.stringify(state), before);
  assert.ok(next !== state, "must return a new object");
  assert.ok(next.types !== typesBefore, "types must be a copy");
  const dropped = withoutField(state, "types");
  assert.deepStrictEqual(state.types, ["Fire", "Water"]);
  assert.ok(dropped.types !== defaults.types, "must not hand out the defaults' list");
});

test("withoutField with a field the page does not hold returns an equal copy", () => {
  load(FULL);
  const next = withoutField(state, "page_size");
  assert.ok(next !== state, "must return a new object");
  assert.equal(JSON.stringify(next), JSON.stringify(state));
  assert.equal(Object.hasOwn(next, "page_size"), false);
});

// ignoredNotice

test("ignoredNotice names one ignored parameter", () => {
  assert.equal(ignoredNotice([{ field: "sort", value: "bogus" }]), 'Ignored "sort=bogus": not a valid value.');
});

test("ignoredNotice names two and three parameters in one sentence", () => {
  assert.equal(
    ignoredNotice([{ field: "order", value: "sideways" }, { field: "supertype", value: "wizard" }]),
    'Ignored "order=sideways" and "supertype=wizard": not valid values.',
  );
  assert.equal(
    ignoredNotice([{ field: "a", value: "1" }, { field: "b", value: "2" }, { field: "c", value: "3" }]),
    'Ignored "a=1", "b=2" and "c=3": not valid values.',
  );
});

test("ignoredNotice shortens a very long value", () => {
  const text = ignoredNotice([{ field: "set", value: "x".repeat(500) }]);
  assert.ok(text.length < 120, `too long: ${text.length}`);
  assert.ok(text.includes("…"), "a shortened value is marked");
});

test("ignoredNotice of nothing is empty", () => {
  assert.equal(ignoredNotice([]), "");
});

// failureAction

test("a network failure or a 5xx is an outage", () => {
  load("?q=pika&sort=bogus");
  for (const status of [0, undefined, null, 500, 502, 503, 504]) {
    assert.deepStrictEqual(failureAction({ status, body: envelope("sort"), state }), { action: "outage" }, `status ${status}`);
  }
});

test("a 400 naming a field the page holds drops it and retries", () => {
  load("?q=pikachu&sort=bogus");
  assert.deepStrictEqual(failureAction({ status: 400, body: envelope("sort"), state }), {
    action: "retry",
    field: "sort",
    value: "bogus",
  });
});

test("a 400 naming types retries with the list as it was sent", () => {
  load("?types=Fire,Bogus");
  assert.deepStrictEqual(failureAction({ status: 400, body: envelope("types"), state }), {
    action: "retry",
    field: "types",
    value: "Fire,Bogus",
  });
});

test("every filter, sort and order field is droppable", () => {
  load(FULL);
  for (const field of ["supertype", "types", "set", "rarity", "series", "sort", "order"]) {
    assert.equal(failureAction({ status: 400, body: envelope(field), state }).action, "retry", field);
  }
});

test("a second bad field in the same search is dropped too, but a field never twice", () => {
  load("?q=pikachu&supertype=wizard");
  const dropped = [{ field: "order", value: "sideways" }];
  assert.deepStrictEqual(failureAction({ status: 400, body: envelope("supertype"), state, dropped }), {
    action: "retry",
    field: "supertype",
    value: "wizard",
  });
  const again = failureAction({ status: 400, body: envelope("supertype", "still bad"), state, dropped: [{ field: "supertype", value: "wizard" }] });
  assert.equal(again.action, "notice");
  assert.equal(again.notice, 'Ignored "supertype=wizard": not a valid value. PokéSearch could not run that search: still bad.');
});

test("a 400 with field q keeps the text and says the search is too long, with no retry", () => {
  load(`?q=${"pika ".repeat(40)}`);
  const result = failureAction({ status: 400, body: envelope("q"), state });
  assert.deepStrictEqual(result, { action: "notice", notice: "That search is too long to run. Try a shorter one." });
});

test("a 400 with field q after a dropped field names both", () => {
  load("?q=long");
  const result = failureAction({ status: 400, body: envelope("q"), state, dropped: [{ field: "sort", value: "bogus" }] });
  assert.equal(result.action, "notice");
  assert.equal(result.notice, 'Ignored "sort=bogus": not a valid value. That search is too long to run. Try a shorter one.');
});

test("a 400 naming no field, an unknown field or an empty field ends in a notice", () => {
  load("?q=pikachu");
  const cases = [
    [envelope(undefined, "no good"), "PokéSearch could not run that search: no good."],
    [envelope("page_size", "page_size must be 1-100"), "PokéSearch could not run that search: page_size must be 1-100."],
    [envelope("sort", "sort must be one of relevance|newest."), "PokéSearch could not run that search: sort must be one of relevance|newest."],
    [{ error: "not an object" }, "PokéSearch could not run that search."],
    [null, "PokéSearch could not run that search."],
    ["<html>", "PokéSearch could not run that search."],
  ];
  for (const [body, notice] of cases) {
    assert.deepStrictEqual(failureAction({ status: 400, body, state }), { action: "notice", notice });
  }
});

test("other 4xx statuses are not outages and never retry", () => {
  load("?q=pikachu&sort=bogus");
  for (const status of [401, 404, 405, 429]) {
    const result = failureAction({ status, body: envelope("sort", "nope"), state });
    assert.equal(result.action, "notice", `status ${status}`);
  }
});

test("failureAction does not mutate the state or the dropped list", () => {
  load("?q=pikachu&sort=bogus");
  const dropped = [{ field: "order", value: "x" }];
  const before = JSON.stringify(state);
  assert.equal(failureAction({ status: 400, body: envelope("sort"), state, dropped }).action, "retry");
  assert.equal(JSON.stringify(state), before);
  assert.equal(dropped.length, 1);
});

// historyAction

test("the first typing search after a committed entry pushes; later ones replace", () => {
  assert.equal(historyAction("typing", { draft: false, changed: true }), "push");
  assert.equal(historyAction("typing", { draft: true, changed: true }), "replace");
  assert.equal(historyAction("typing", { draft: true, changed: false }), "replace");
  assert.equal(historyAction("typing", { draft: false, changed: false }), "replace");
});

test("Enter, blur and idle commit: a draft is kept in place, a new query pushes, the same query does nothing", () => {
  for (const trigger of ["enter", "blur", "idle"]) {
    assert.equal(historyAction(trigger, { draft: true, changed: false }), "none", trigger);
    assert.equal(historyAction(trigger, { draft: true, changed: true }), "replace", trigger);
    assert.equal(historyAction(trigger, { draft: false, changed: true }), "push", trigger);
    assert.equal(historyAction(trigger, { draft: false, changed: false }), "none", trigger);
  }
});

test("suggestions and filters push, popstate writes nothing, anything else replaces", () => {
  for (const draft of [true, false]) {
    for (const changed of [true, false]) {
      assert.equal(historyAction("suggestion", { draft, changed }), "push");
      assert.equal(historyAction("filter", { draft, changed }), "push");
      assert.equal(historyAction("popstate", { draft, changed }), "none");
      assert.equal(historyAction("retry", { draft, changed }), "replace");
      assert.equal(historyAction("refresh", { draft, changed }), "replace");
    }
  }
});

test("typing a whole word slowly then going idle adds one entry", () => {
  // Simulate the page: one debounced search per key, then an idle commit.
  let draft = false;
  let entries = 1;
  let address = "";
  for (let i = 1; i <= "charizard".length; i++) {
    const next = "charizard".slice(0, i);
    const action = historyAction("typing", { draft, changed: next !== address });
    if (action === "push") { entries += 1; draft = true; }
    address = next;
  }
  const commit = historyAction("idle", { draft, changed: false });
  assert.equal(commit, "none");
  draft = false;
  // A second commit of the same query does nothing either.
  assert.equal(historyAction("enter", { draft, changed: false }), "none");
  assert.equal(entries, 2);
});
