import test from "node:test";
import assert from "node:assert/strict";
import { historyStep, freshHistoryMemo } from "./state.js";

// A browser history stack with the page on top of it, wired the way api.js
// and main.js wire historyStep: the debounce and idle timers, commitQuery,
// the page's own step back and the popstate listener with isOwnStepBack.
// Each entry is an object, so the test can tell which entry a write touched.

function deepFreeze(value) {
  if (value && typeof value === "object") {
    for (const inner of Object.values(value)) deepFreeze(inner);
    Object.freeze(value);
  }
  return value;
}

function browser(start) {
  let nextId = 0;
  const entry = (query, finished) => ({ id: nextId++, query, finished });
  const stack = [entry(start, true)];
  let index = 0;
  let memo = freshHistoryMemo();
  let stepBackTo = null;
  let debouncePending = false;
  let idlePending = false;
  // The entry the open typing burst pushed, if any.
  let draftEntry = null;
  let text = start.replace(/^q=/, "").split("&")[0];
  let filters = start.includes("&") ? start.slice(start.indexOf("&")) : "";
  const log = [];

  const wanted = () => `q=${text}${filters}`;
  const current = () => stack[index];

  function plan(trigger) {
    const given = deepFreeze({ ...memo });
    const result = historyStep({ trigger, memo: given, address: current().query, wanted: wanted() });
    assert.ok(["push", "replace", "back", "none"].includes(result.action), `${trigger}: ${result.action}`);
    return result;
  }

  function popstate() {
    // main.js: the page's own step back is ignored; anything else is the
    // visitor navigating.
    const expected = stepBackTo;
    stepBackTo = null;
    if (expected !== null && expected === current().query) return;
    debouncePending = false;
    idlePending = false;
    memo = plan("popstate").memo;
    assert.deepStrictEqual(memo, freshHistoryMemo(), "popstate must forget the draft");
    draftEntry = null;
    const [, rest = ""] = current().query.split("q=");
    text = rest.split("&")[0];
    filters = rest.includes("&") ? rest.slice(rest.indexOf("&")) : "";
    current().finished = true;
  }

  function write(trigger) {
    const before = { index, query: current().query, id: current().id };
    const { action, memo: next } = plan(trigger);
    log.push(`${trigger}:${action}`);
    if (action === "back") {
      assert.ok(index > 0, `${trigger} stepped back from the first entry`);
      assert.ok(draftEntry && current() === draftEntry, `${trigger} stepped back from an entry no burst pushed`);
      assert.equal(stack[index - 1].query, memo.below, `${trigger} stepped back onto the wrong entry`);
      assert.equal(stack[index - 1].query, wanted(), "the entry beneath does not hold the query");
    }
    if (trigger === "popstate") assert.equal(action, "none");
    memo = next;
    if (action === "push" || action === "replace") {
      // writeStateToURL downgrades a push of an unchanged address to a replace.
      if (action === "push" && wanted() !== current().query) {
        stack.splice(index + 1, Infinity, entry(wanted(), trigger !== "typing"));
        index++;
        if (trigger === "typing") draftEntry = current();
      } else {
        if (current().finished && trigger === "typing") {
          assert.equal(wanted(), current().query, `typing rewrote a finished entry ${before.query}`);
        }
        current().query = wanted();
      }
    } else if (action === "back") {
      stepBackTo = wanted();
      index--;
      popstate();
    }
    if (trigger !== "typing" && trigger !== "refresh") {
      current().finished = true;
      draftEntry = null;
    }
    if (action === "back") draftEntry = null;
  }

  function commit(trigger) {
    idlePending = false;
    const { action, memo: next } = plan(trigger);
    if (action === "none") {
      memo = next;
      draftEntry = null;
      current().finished = true;
      return;
    }
    debouncePending = false;
    write(trigger);
  }

  function key(next) {
    text = next;
    // The input listener clears an explicit sort with every keystroke.
    filters = filters.replace(/&sort=[^&]*/, "");
    debouncePending = true;
    idlePending = true;
  }

  const events = {
    type: () => key(`${text}x`),
    erase: () => key(text.slice(0, -1)),
    debounce: () => {
      if (!debouncePending) return;
      debouncePending = false;
      write("typing");
    },
    idle: () => {
      // The idle timer is five debounces long: it never fires first.
      if (!idlePending || debouncePending) return;
      commit("idle");
      assert.equal(current().query, wanted(), "the address did not catch up with the box at idle");
    },
    enter: () => commit("enter"),
    blur: () => commit("blur"),
    pickSame: () => {
      debouncePending = false;
      idlePending = false;
      write("filter");
    },
    pickOther: () => {
      debouncePending = false;
      idlePending = false;
      text = `${text}zz`;
      write("filter");
    },
    filter: () => {
      filters = filters.includes("&set=a") ? filters.replace("&set=a", "") : `${filters}&set=a`;
      write("filter");
    },
    back: () => {
      if (index === 0) return;
      index--;
      popstate();
    },
    forward: () => {
      if (index === stack.length - 1) return;
      index++;
      popstate();
    },
    retry: () => {
      // A search the page runs again without a new history step: the retry
      // after a refused parameter, Load more, Try again. Every value here is
      // one the server takes (a refused one is dropped by the very first
      // search that carries it), so the state is unchanged. The refusal
      // answers the search that just ran, so it is modelled only when no
      // keystroke is waiting on the debounce.
      if (debouncePending) return;
      write("refresh");
    },
  };

  function check(sequence) {
    for (let i = 1; i < stack.length; i++) {
      assert.notEqual(stack[i].query, stack[i - 1].query,
        `adjacent identical entries after ${sequence.join(",")}: ${stack.map((e) => e.query).join(" | ")}`);
    }
  }

  return { events, check, log, stack };
}

function* sequences(names, length) {
  if (length === 0) {
    yield [];
    return;
  }
  for (const rest of sequences(names, length - 1)) {
    for (const name of names) yield [...rest, name];
  }
}

const EVENTS = ["type", "erase", "debounce", "idle", "enter", "blur", "pickSame", "pickOther", "filter", "back", "forward", "retry"];

test("every run of five events keeps history honest", () => {
  for (const start of ["q=mew", "q=mew&sort=hp", "q="]) {
    for (let length = 1; length <= 5; length++) {
      for (const sequence of sequences(EVENTS, length)) {
        const page = browser(start);
        for (const name of sequence) {
          page.events[name]();
          page.check(sequence);
        }
      }
    }
  }
});

test("longer seeded runs of keystrokes, timers and navigation keep history honest", () => {
  // A small deterministic generator, so a failure names a sequence that can be replayed.
  let seed = 12345;
  const next = () => {
    seed = (seed * 1103515245 + 12345) % 2147483648;
    return seed;
  };
  const typing = ["type", "type", "erase", "debounce", "debounce", "idle"];
  for (let run = 0; run < 20000; run++) {
    const page = browser("q=mew");
    const sequence = [];
    for (let i = 0; i < 12; i++) {
      const pool = next() % 3 === 0 ? EVENTS : typing;
      const name = pool[next() % pool.length];
      sequence.push(name);
      page.events[name]();
      page.check(sequence);
    }
  }
});

test("one draft steps back at most once", () => {
  // From mew: type x, search, erase, search (steps back), then type and erase
  // again without a pause. The second burst opens a new draft over mew, so the
  // second step back lands on the same entry and the stack never grows.
  const page = browser("q=mew");
  const { events } = page;
  events.type();
  events.debounce();
  events.erase();
  events.debounce();
  assert.deepStrictEqual(page.log, ["typing:push", "typing:back"]);
  assert.deepStrictEqual(page.stack.map((e) => e.query), ["q=mew", "q=mewx"]);
  events.debounce();
  events.idle();
  assert.deepStrictEqual(page.log, ["typing:push", "typing:back"], "a second write for the same draft");
  events.type();
  events.debounce();
  events.erase();
  events.debounce();
  assert.deepStrictEqual(page.stack.map((e) => e.query), ["q=mew", "q=mewx"]);
});

test("no step back after the visitor navigated away from the draft", () => {
  const page = browser("q=mew");
  const { events } = page;
  events.type();
  events.debounce();
  events.back();
  events.forward();
  // Now on the old draft's entry, but it belongs to history, not to a burst.
  events.erase();
  events.debounce();
  assert.ok(!page.log.includes("typing:back"), page.log.join(","));
});

test("hostile inputs that the page could hand over never throw and never push for an unknown trigger", () => {
  const memos = [{}, { draft: true }, { draft: true, below: undefined }, { draft: "yes", below: 5 }, freshHistoryMemo()];
  const values = ["", "q=mew", "q=mew#stats", "q=mew&set=a", "set=a&q=mew", undefined, null, 7, ["q=mew"]];
  for (const memo of memos) {
    for (const address of values) {
      for (const wanted of values) {
        for (const trigger of ["typing", "enter", "blur", "idle", "suggestion", "filter", "popstate", "refresh", "", undefined, "__proto__", "TYPING"]) {
          const frozen = deepFreeze(structuredClone(memo));
          const result = historyStep({ trigger, memo: frozen, address, wanted });
          assert.ok(["push", "replace", "back", "none"].includes(result.action));
          assert.ok(result.memo && typeof result.memo === "object", `${String(trigger)} lost the memo`);
          assert.ok(result.memo !== frozen, `${String(trigger)} handed back the memo it was given`);
          const known = ["typing", "enter", "blur", "idle", "suggestion", "filter"].includes(trigger);
          if (!known) assert.notEqual(result.action, "push", `${String(trigger)} pushed`);
        }
      }
    }
  }
});

test("an unknown trigger steps back only onto an entry that already holds the query", () => {
  // Stepping back is the alternative to writing an entry equal to the one
  // beneath, whatever started the search, so a retry or refresh may take it.
  const memo = { draft: true, below: "q=mew" };
  assert.equal(historyStep({ trigger: "refresh", memo, address: "q=mewx", wanted: "q=mew" }).action, "back");
  assert.equal(historyStep({ trigger: "refresh", memo, address: "q=mewx", wanted: "q=mewy" }).action, "replace");
  assert.equal(historyStep({ trigger: "refresh", memo, address: "q=mew", wanted: "q=mew" }).action, "replace");
});
