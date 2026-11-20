import test from "node:test";
import assert from "node:assert/strict";
import {
  SEARCH_PAGE_SIZE,
  searchRequestParams,
  stepBackHolds,
  searchBoxStatus,
  isOwnStepBack,
  scheduleSearch,
  cancelScheduledSearch,
} from "./api.js";
import { readStateFromURL, buildParams, state, historyStep, freshHistoryMemo, writeStateToURL } from "./state.js";

function at(search) {
  globalThis.location = { search, pathname: "/", hash: "" };
  readStateFromURL();
}

test.afterEach(() => {
  cancelScheduledSearch();
  at("");
  delete globalThis.location;
  delete globalThis.history;
});

// searchRequestParams over every shape of state the page can hold

const SHAPES = [
  ["a browse", ""],
  ["a query", "?q=pikachu"],
  ["a query with characters a querystring treats as syntax", `?q=${encodeURIComponent("a&b=c#d é 🔥")}`],
  ["every filter", "?q=pika&supertype=Pok%C3%A9mon&types=Fire,Water&set=base1&rarity=Rare&series=Base"],
  ["a sort and an order", "?q=pika&sort=hp&order=asc"],
  ["filters and no query", "?types=Fire&rarity=Rare%20Holo"],
];

test("the search request is the state's querystring plus exactly one page_size, whatever the state holds", () => {
  for (const [label, search] of SHAPES) {
    for (const page of [1, 2, 400]) {
      for (const debug of [false, true]) {
        at(search);
        state.page = page;
        const plain = buildParams({ page: true, debug });
        const request = searchRequestParams({ debug });
        const entries = [...request];
        assert.equal(entries.filter(([key]) => key === "page_size").length, 1, `${label} ${page} ${debug}: one page_size`);
        assert.equal(request.get("page_size"), String(SEARCH_PAGE_SIZE), `${label} ${page} ${debug}`);
        // Everything else is the state's own request, in the same order.
        assert.deepStrictEqual(
          entries.filter(([key]) => key !== "page_size"),
          [...plain],
          `${label} ${page} ${debug}: nothing dropped, added or reordered`,
        );
        assert.equal(request.get("page"), page > 1 ? String(page) : null, `${label} ${page}: Load more asks for its page`);
        assert.equal(request.get("debug"), debug ? "1" : null, `${label} ${page} ${debug}`);
      }
    }
  }
});

test("asking for a request leaves the address querystring and the state without a page size", () => {
  at("?q=pikachu&types=Fire");
  const writes = [];
  globalThis.history = {
    state: null,
    pushState: (_state, _title, url) => writes.push(url),
    replaceState: (_state, _title, url) => writes.push(url),
  };
  searchRequestParams({ debug: true });
  searchRequestParams();
  writeStateToURL({ push: true });
  assert.equal(buildParams().toString(), "q=pikachu&types=Fire");
  assert.equal(buildParams({ page: true, debug: true }).has("page_size"), false, "the state does not carry it");
  assert.ok(!Object.hasOwn(state, "page_size"));
  for (const url of writes) assert.ok(!url.includes("page_size"), url);
});

test("a hand-edited address cannot make the request ask for a size other than the one the skeleton draws", () => {
  for (const search of ["?q=pika&page_size=5", "?page_size=100&q=pika", "?q=pika&page_size=24&page_size=1", "?q=pika&PAGE_SIZE=3", "?q=pika&page=9&page_size=0"]) {
    at(search);
    const request = searchRequestParams();
    assert.deepStrictEqual(request.getAll("page_size"), [String(SEARCH_PAGE_SIZE)], search);
    // The page the address names is not a state field either: Load more owns it.
    assert.equal(request.get("page"), null, search);
  }
});

test("a state with its page set below one still asks for the first page", () => {
  at("?q=pika");
  for (const page of [0, -3, 1]) {
    state.page = page;
    assert.equal(searchRequestParams().get("page"), null, String(page));
  }
});

// stepBackHolds

test("with no step back under way the guard holds nothing back, whatever the query", () => {
  for (const wanted of ["", "q=pika", "q=Pika", " ", "q=", "null", "undefined", "0", null, "q=a&sort=hp"]) {
    assert.equal(stepBackHolds({ stepBackTo: null, wanted }), false, String(wanted));
  }
});

test("the guard holds back only a commit for exactly the query the step back lands on", () => {
  const heading = "q=pika&types=Fire";
  const different = [
    "q=Pika&types=Fire",
    "q=pika&types=fire",
    " q=pika&types=Fire",
    "q=pika&types=Fire ",
    "q=pika&types=Fire&sort=hp",
    "types=Fire&q=pika",
    "q=pika",
    "",
    "q=pika+&types=Fire",
  ];
  assert.equal(stepBackHolds({ stepBackTo: heading, wanted: heading }), true);
  for (const wanted of different) assert.equal(stepBackHolds({ stepBackTo: heading, wanted }), false, JSON.stringify(wanted));
  assert.equal(stepBackHolds({ stepBackTo: "", wanted: "q=pika" }), false, "a step back to a browse holds no search");
  assert.equal(stepBackHolds({ stepBackTo: "q=pika", wanted: "" }), false, "and no browse holds a search");
});

test("nothing the page remembers makes the guard hold after the step back has landed", () => {
  // isOwnStepBack is what clears the flag; with none set it answers false, every time.
  at("?q=pika");
  for (let i = 0; i < 3; i += 1) assert.equal(isOwnStepBack(), false);
  assert.equal(searchBoxStatus().changed, false);
});

// searchBoxStatus: what Enter reads

test("the search box is changed only when the query it holds differs from the address's", () => {
  at("?q=pika");
  assert.equal(searchBoxStatus().changed, false, "the address holds it");
  state.q = "pika  ";
  assert.equal(searchBoxStatus().changed, false, "surrounding spaces are not a different query");
  state.q = "pikac";
  assert.equal(searchBoxStatus().changed, true);
  state.q = "Pika";
  assert.equal(searchBoxStatus().changed, true, "a different case is a different query");
  state.q = "";
  assert.equal(searchBoxStatus().changed, true, "an emptied box is a different query from the one shown");
  at("");
  assert.deepStrictEqual(searchBoxStatus(), { pending: false, changed: false }, "an empty box on an empty address");
  state.q = "   ";
  assert.equal(searchBoxStatus().changed, false, "spaces alone are an empty query");
});

test("a changed filter counts as a changed query, so Enter does not skip the search", () => {
  at("?q=pika");
  state.types = ["Fire"];
  assert.equal(searchBoxStatus().changed, true);
  state.types = [];
  state.sort = "hp";
  assert.equal(searchBoxStatus().changed, true);
});

test("a keystroke search is pending from the first keystroke until it is cancelled, and a burst is one", () => {
  at("?q=pika");
  assert.equal(searchBoxStatus().pending, false);
  for (let i = 0; i < 20; i += 1) scheduleSearch();
  assert.equal(searchBoxStatus().pending, true);
  cancelScheduledSearch();
  assert.equal(searchBoxStatus().pending, false);
  cancelScheduledSearch();
  assert.equal(searchBoxStatus().pending, false, "cancelling twice leaves nothing pending");
});

// The guard and the history decision together: a page's own step back that has
// been asked for but has not landed yet.

// A history stack with the page on top, wired as api.js wires it, except that
// the step back is a separate event: the address keeps showing the draft until
// land() is called, and Enter, blur and idle commits can arrive in between.
function pageWithSlowStepBack() {
  const stack = [""];
  let index = 0;
  let memo = freshHistoryMemo();
  let stepBackTo = null;
  let landing = null;
  let text = "";
  let writes = 0;
  const wanted = () => (text.trim() ? `q=${text.trim()}` : "");
  const address = () => stack[index];

  function writeHistory(trigger) {
    const { action, memo: next } = historyStep({ trigger, memo, address: address(), wanted: wanted() });
    memo = next;
    if (action === "push" && wanted() !== address()) {
      stack.splice(index + 1, Infinity, wanted());
      index += 1;
      writes += 1;
    } else if (action === "push" || action === "replace") {
      if (stack[index] !== wanted()) writes += 1;
      stack[index] = wanted();
    } else if (action === "back") {
      stepBackTo = wanted();
      landing = index - 1;
    }
  }

  return {
    stack,
    get index() { return index; },
    get address() { return address(); },
    get writes() { return writes; },
    get stepping() { return stepBackTo !== null; },
    type(next) { text = next; },
    debounce() { writeHistory("typing"); },
    // commitQuery, exactly as api.js orders it.
    commit(trigger) {
      if (stepBackHolds({ stepBackTo, wanted: wanted() })) return;
      const { action, memo: next } = historyStep({ trigger, memo, address: address(), wanted: wanted() });
      if (action === "none") {
        memo = next;
        return;
      }
      writeHistory(trigger);
    },
    land() {
      if (landing === null) return;
      index = landing;
      landing = null;
      const expected = stepBackTo;
      stepBackTo = null;
      if (expected !== null && expected === address()) return;
      memo = freshHistoryMemo();
      text = address().replace(/^q=/, "");
    },
  };
}

test("commits that arrive before the page's own step back lands push nothing, in any order", () => {
  const orders = [
    ["enter"], ["blur"], ["idle"],
    ["enter", "blur"], ["blur", "enter"], ["enter", "enter"], ["blur", "blur", "idle"], ["idle", "enter", "blur"],
  ];
  for (const order of orders) {
    const page = pageWithSlowStepBack();
    page.type("pika");
    page.debounce();
    page.commit("idle");
    page.type("pikax");
    page.debounce();
    assert.deepStrictEqual(page.stack, ["", "q=pika", "q=pikax"], "a draft on top of the finished entry");
    page.type("pika");
    page.debounce();
    assert.equal(page.stepping, true, "typing back to the entry beneath asks to step back");
    const writesBefore = page.writes;
    for (const trigger of order) page.commit(trigger);
    assert.equal(page.writes, writesBefore, `${order.join(",")}: nothing written while the step back is on its way`);
    page.land();
    assert.equal(page.index, 1, order.join(","));
    assert.equal(page.address, "q=pika", order.join(","));
    assert.deepStrictEqual(page.stack, ["", "q=pika", "q=pikax"], `${order.join(",")}: no second entry`);
    // After it lands the flag is gone: the same commits are ordinary ones that find nothing to write.
    for (const trigger of order) page.commit(trigger);
    assert.equal(page.writes, writesBefore, `${order.join(",")}: still nothing to write`);
  }
});

test("a commit the visitor needs after the step back is never swallowed", () => {
  for (const trigger of ["enter", "blur", "idle"]) {
    const page = pageWithSlowStepBack();
    page.type("pika");
    page.debounce();
    page.commit("idle");
    page.type("pikax");
    page.debounce();
    page.type("pika");
    page.debounce();
    page.commit("enter");
    page.land();
    // A different query, committed after the step back has landed, reaches the address.
    page.type("pikay");
    page.commit(trigger);
    assert.equal(page.address, "q=pikay", `${trigger}: the new query is written`);
    assert.equal(page.stepping, false);
    // And typing back to the first query again is a plain commit, not held by a stale flag.
    page.type("pika");
    page.commit(trigger);
    assert.equal(page.address, "q=pika", `${trigger}: the first query is written again`);
  }
});

test("Enter pressed while the debounce still waits steps back once, and the blur after it writes nothing", () => {
  const page = pageWithSlowStepBack();
  page.type("pika");
  page.debounce();
  page.commit("idle");
  page.type("pikax");
  page.debounce();
  page.type("pika"); // the debounce for this keystroke has not fired
  page.commit("enter");
  assert.equal(page.stepping, true, "Enter finished the query by stepping back");
  const writes = page.writes;
  page.commit("blur"); // focus moves to the results
  assert.equal(page.writes, writes, "the blur wrote nothing");
  page.land();
  assert.deepStrictEqual(page.stack, ["", "q=pika", "q=pikax"]);
  assert.equal(page.index, 1);
});
