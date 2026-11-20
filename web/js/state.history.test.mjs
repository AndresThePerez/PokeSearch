import test from "node:test";
import assert from "node:assert/strict";
import { historyStep, freshHistoryMemo } from "./state.js";

const FRESH = { draft: false, below: null };

function step(trigger, memo, address, wanted) {
  return historyStep({ trigger, memo, address, wanted });
}

// historyStep, case by case

test("a fresh memo has no draft entry", () => {
  assert.deepStrictEqual(freshHistoryMemo(), FRESH);
  assert.ok(freshHistoryMemo() !== freshHistoryMemo(), "each call returns a new object");
});

test("the first typing search on a finished entry pushes and remembers the address beneath it", () => {
  assert.deepStrictEqual(step("typing", FRESH, "q=mew", "q=mewx"), {
    action: "push",
    memo: { draft: true, below: "q=mew" },
  });
});

test("later typing searches rewrite the draft entry", () => {
  const memo = { draft: true, below: "q=mew" };
  assert.deepStrictEqual(step("typing", memo, "q=mewx", "q=mewxy"), { action: "replace", memo });
});

test("typing back to the address beneath the draft steps back to it", () => {
  assert.deepStrictEqual(step("typing", { draft: true, below: "q=mew" }, "q=mewx", "q=mew"), {
    action: "back",
    memo: FRESH,
  });
});

test("a commit that lands on the address beneath the draft steps back too", () => {
  for (const trigger of ["enter", "blur", "idle"]) {
    assert.deepStrictEqual(step(trigger, { draft: true, below: "q=mew" }, "q=mewx", "q=mew"), {
      action: "back",
      memo: FRESH,
    }, trigger);
  }
});

test("typing that does not change the address on a finished entry rewrites it and opens no draft", () => {
  assert.deepStrictEqual(step("typing", FRESH, "q=mew", "q=mew"), { action: "replace", memo: FRESH });
});

test("Enter, blur and idle close the draft whatever they write", () => {
  for (const trigger of ["enter", "blur", "idle"]) {
    assert.deepStrictEqual(step(trigger, { draft: true, below: "" }, "q=pika", "q=pika"), { action: "none", memo: FRESH }, trigger);
    assert.deepStrictEqual(step(trigger, { draft: true, below: "" }, "q=pika", "q=pikachu"), { action: "replace", memo: FRESH }, trigger);
    assert.deepStrictEqual(step(trigger, FRESH, "q=pika", "q=pikachu"), { action: "push", memo: FRESH }, trigger);
    assert.deepStrictEqual(step(trigger, FRESH, "q=pika", "q=pika"), { action: "none", memo: FRESH }, trigger);
  }
});

test("a suggestion or filter closes the draft even when the address already holds it", () => {
  for (const trigger of ["suggestion", "filter"]) {
    assert.deepStrictEqual(step(trigger, { draft: true, below: "q=mew" }, "q=Pikachu", "q=Pikachu"), {
      action: "replace",
      memo: FRESH,
    }, trigger);
    assert.deepStrictEqual(step(trigger, { draft: true, below: "q=mew" }, "q=Pika", "q=Pikachu"), {
      action: "push",
      memo: FRESH,
    }, trigger);
  }
});

test("popstate writes nothing and forgets the draft", () => {
  assert.deepStrictEqual(step("popstate", { draft: true, below: "q=mew" }, "q=mew", "q=mewx"), { action: "none", memo: FRESH });
});

test("a retry or refresh only rewrites and keeps the memo as it was", () => {
  const memo = { draft: true, below: "q=mew" };
  for (const trigger of ["refresh", "retry", "", undefined]) {
    assert.deepStrictEqual(step(trigger, memo, "q=mewx&sort=bogus", "q=mewx"), { action: "replace", memo }, String(trigger));
  }
});

test("historyStep never mutates the memo it is given", () => {
  const memo = Object.freeze({ draft: true, below: "q=mew" });
  for (const trigger of ["typing", "enter", "blur", "idle", "suggestion", "filter", "popstate", "refresh"]) {
    for (const wanted of ["q=mew", "q=other"]) {
      const result = step(trigger, memo, "q=mewx", wanted);
      assert.ok(["push", "replace", "back", "none"].includes(result.action), `${trigger}: ${result.action}`);
      assert.ok(result.memo !== memo || trigger === "refresh", `${trigger} handed back the same memo`);
    }
  }
  assert.deepStrictEqual(memo, { draft: true, below: "q=mew" });
});

// The page, played through historyStep. A browser history stack, the address,
// and the box: each event does what api.js and main.js do with the step.

function page(start = "") {
  const stack = [start];
  let index = 0;
  let memo = freshHistoryMemo();
  let wanted = start;
  const writes = [];
  function carry(trigger) {
    const result = historyStep({ trigger, memo, address: stack[index], wanted });
    memo = result.memo;
    writes.push(result.action);
    if (result.action === "push") {
      stack.splice(index + 1, Infinity, wanted);
      index++;
    } else if (result.action === "replace") {
      stack[index] = wanted;
    } else if (result.action === "back") {
      // Our own step back: the popstate it fires is ignored by the page.
      index--;
    }
  }
  return {
    stack,
    writes,
    get address() {
      return stack[index];
    },
    get wanted() {
      return wanted;
    },
    // One debounced search per call, as typing slowly gives.
    type(text) {
      wanted = text;
      carry("typing");
    },
    commit(trigger) {
      carry(trigger);
    },
    pick(text) {
      wanted = text;
      carry("suggestion");
    },
    filter(tag) {
      wanted = `${wanted}&${tag}`;
      carry("filter");
    },
    back() {
      index--;
      wanted = stack[index];
      const before = writes.length;
      carry("popstate");
      assert.equal(writes[before], "none", "popstate wrote history");
    },
    forward() {
      index++;
      wanted = stack[index];
      carry("popstate");
    },
  };
}

test("charizard typed a key at a time from mew adds one entry; Back and Forward walk it", () => {
  const p = page("q=mew");
  for (let i = 1; i <= "charizard".length; i++) p.type(`q=${"charizard".slice(0, i)}`);
  p.commit("idle");
  assert.deepStrictEqual(p.stack, ["q=mew", "q=charizard"]);
  p.back();
  assert.equal(p.address, "q=mew");
  p.forward();
  assert.equal(p.address, "q=charizard");
});

test("a burst that returns to the text it started from leaves no entry and no identical pair", () => {
  const p = page("q=mew");
  p.type("q=mewx");
  p.type("q=mew");
  p.commit("idle");
  assert.deepStrictEqual(p.stack.slice(0, 1), ["q=mew"]);
  assert.equal(p.address, "q=mew");
  // The draft entry may still sit ahead as Forward history, but the next
  // write replaces it, so it never lands beside its twin.
  p.type("q=ditto");
  p.commit("idle");
  assert.deepStrictEqual(p.stack, ["q=mew", "q=ditto"]);
});

test("a picked suggestion equal to the typed text stays in history when typing follows", () => {
  const p = page("q=mew");
  for (const text of ["q=P", "q=Pika", "q=Pikachu"]) p.type(text);
  p.pick("q=Pikachu");
  for (const text of ["q=r", "q=rai", "q=raichu"]) p.type(text);
  p.commit("idle");
  assert.deepStrictEqual(p.stack, ["q=mew", "q=Pikachu", "q=raichu"]);
  p.back();
  assert.equal(p.address, "q=Pikachu");
});

test("every finishing action makes the typing after it start a new entry", () => {
  const finishers = [
    (p) => p.commit("enter"),
    (p) => p.commit("blur"),
    (p) => p.commit("idle"),
    (p) => p.pick("q=pika"),
    (p) => p.filter("types=Fire"),
  ];
  for (const finish of finishers) {
    const p = page("");
    p.type("q=pi");
    p.type("q=pika");
    finish(p);
    const finished = p.address;
    p.type(`${finished}x`);
    p.commit("idle");
    assert.ok(p.stack.includes(finished), `the finished entry ${finished} was rewritten: ${p.stack}`);
    assert.equal(p.stack[p.stack.length - 1], `${finished}x`);
  }
});

test("committing the same query twice adds nothing the second time", () => {
  const p = page("");
  p.type("q=mew");
  p.commit("enter");
  const length = p.stack.length;
  p.commit("enter");
  p.commit("blur");
  p.commit("idle");
  assert.equal(p.stack.length, length);
  assert.deepStrictEqual(p.writes.slice(-3), ["none", "none", "none"]);
});

test("an idle commit still pending when the visitor goes Back writes nothing", () => {
  const p = page("q=mew");
  p.type("q=mewtwo");
  p.back();
  // The timer fires now, against the entry Back landed on.
  p.commit("idle");
  assert.equal(p.writes[p.writes.length - 1], "none");
  assert.deepStrictEqual(p.stack, ["q=mew", "q=mewtwo"]);
  assert.equal(p.address, "q=mew");
});

test("no adjacent identical entries after any short run of typing and finishing", () => {
  const texts = ["q=mew", "q=mewx", "q=mewxy", "q=a"];
  const events = [
    ...texts.map((text) => (p) => p.type(text)),
    (p) => p.commit("enter"),
    (p) => p.commit("idle"),
    (p) => p.pick("q=mewx"),
  ];
  const run = (sequence) => {
    const p = page("q=mew");
    for (const index of sequence) events[index](p);
    // The address always catches up with what the box holds.
    assert.equal(p.address, p.wanted, `address behind after ${sequence}`);
    for (let i = 1; i <= p.stack.length - 1; i++) {
      assert.notEqual(p.stack[i], p.stack[i - 1], `identical neighbours after ${sequence}: ${p.stack}`);
    }
  };
  const all = [[]];
  for (let length = 1; length <= 4; length++) {
    for (const prefix of all.filter((sequence) => sequence.length === length - 1)) {
      for (let e = 0; e < events.length; e++) all.push([...prefix, e]);
    }
  }
  for (const sequence of all) run(sequence);
});
