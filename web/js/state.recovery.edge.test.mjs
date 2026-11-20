import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
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
  return { error: { code: "invalid_param", field, message }, request_id: "0000000000000000" };
}

// Every field the address can carry, each set to a value the server refuses.
const ALL_BAD = "?q=pika&supertype=wizard&types=Plasma&set=nope&rarity=Mythic&series=Nope&sort=bogus&order=sideways";
const ADDRESS_FIELDS = ["q", "supertype", "types", "set", "rarity", "series", "sort", "order"];

// retryLoop replays what runSearch does with a failed answer: ask failureAction,
// and on "retry" drop the field and go again with it added to the dropped list.
// It returns how many requests went out and how the search ended. The cap is
// far above any legitimate run, so reaching it means the loop does not end.
function retryLoop(start, answer, cap = 50) {
  let current = { ...start, types: [...start.types] };
  let dropped = [];
  for (let request = 1; request <= cap; request++) {
    const reply = answer(current, request);
    if (reply.status === 200) return { requests: request, end: "ok", dropped, state: current };
    const decision = failureAction({ status: reply.status, body: reply.body, state: current, dropped });
    if (decision.action !== "retry") return { requests: request, end: decision.action, dropped, state: current, decision };
    assert.ok(!dropped.some((entry) => entry.field === decision.field), `${decision.field} dropped twice`);
    assert.notEqual(decision.field, "q", "the query text is never dropped");
    const value = decision.field === "types" ? current.types.join(",") : current[decision.field];
    assert.ok(value, `retry on ${decision.field}, which holds no value`);
    current = withoutField(current, decision.field);
    dropped = [...dropped, { field: decision.field, value: decision.value }];
  }
  return { requests: cap + 1, end: "runaway", dropped, state: current };
}

// The first field, in address order, that still holds a value other than q.
function firstBadField(current) {
  return ADDRESS_FIELDS.slice(1).find((field) => (field === "types" ? current.types.length : current[field]));
}

test("a server that refuses one field per answer is satisfied in one retry per field", () => {
  load(ALL_BAD);
  const result = retryLoop(state, (current) => {
    const field = firstBadField(current);
    return field ? { status: 400, body: envelope(field) } : { status: 200 };
  });
  assert.equal(result.end, "ok");
  // Seven droppable fields: seven retries plus the request that succeeded.
  assert.equal(result.requests, 8);
  assert.deepStrictEqual(result.dropped.map((entry) => entry.field), ADDRESS_FIELDS.slice(1));
  assert.equal(result.state.q, "pika", "the query text survives every retry");
});

test("a hostile server can never make the retries run on", () => {
  // Each server answers 400 every time; the search must still end, with a
  // notice, after at most one request per field the page holds.
  const servers = {
    "the same field forever": () => ({ status: 400, body: envelope("sort") }),
    "a field not in the state": () => ({ status: 400, body: envelope("hp_min") }),
    "every field in turn, then again": (_, n) => ({ status: 400, body: envelope(ADDRESS_FIELDS[n % ADDRESS_FIELDS.length]) }),
    "q every time": () => ({ status: 400, body: envelope("q") }),
    "no body at all": () => ({ status: 400, body: null }),
  };
  for (const [name, answer] of Object.entries(servers)) {
    load(ALL_BAD);
    const result = retryLoop(state, answer);
    assert.equal(result.end, "notice", `${name}: ended in ${result.end}`);
    assert.ok(result.requests <= ADDRESS_FIELDS.length, `${name}: ${result.requests} requests`);
  }
});

test("a field the state holds empty is never retried, so the same request is never sent twice", () => {
  load("?q=pika");
  for (const field of ADDRESS_FIELDS) {
    const decision = failureAction({ status: 400, body: envelope(field), state, dropped: [] });
    assert.notEqual(decision.action, "retry", `${field} is empty and must not be retried`);
    assert.equal(decision.action, "notice");
  }
});

test("hostile field names, bodies and statuses never throw and never retry", () => {
  load(ALL_BAD);
  const fields = ["page", "page_size", "debug", "id", "__proto__", "constructor", "toString", "hasOwnProperty",
    "", " sort", "SORT", 7, ["sort"], null, undefined, { toString: () => "sort" }, true];
  for (const field of fields) {
    const decision = failureAction({ status: 400, body: envelope(field), state, dropped: [] });
    assert.equal(decision.action, "notice", `field ${String(field)} gave ${decision.action}`);
    assert.equal(typeof decision.notice, "string");
  }
  const bodies = [null, undefined, "not json", 42, [], [envelope("sort")], {}, { error: null },
    { error: "sort" }, { error: ["sort"] }, { error: { field: null } }, { error: { message: 5 } },
    Object.create(null), { error: Object.assign(Object.create(null), { field: "__proto__" }) }];
  for (const body of bodies) {
    const decision = failureAction({ status: 400, body, state, dropped: [] });
    assert.equal(decision.action, "notice", `body ${JSON.stringify(body)} gave ${decision.action}`);
    assert.equal(typeof decision.notice, "string");
  }
});

test("only a missing status or a 5xx is an outage; any other status is an answer and never retries unless it is a 400", () => {
  load(ALL_BAD);
  const outage = [0, undefined, null, NaN, 500, 502, 503, 504, 599];
  for (const status of outage) {
    assert.equal(failureAction({ status, body: envelope("sort"), state, dropped: [] }).action, "outage", `status ${status}`);
  }
  // A 400 whose body looks like an outage is still the visitor's input.
  const outageLooking = { error: { code: "es_unavailable", message: "search backend unavailable" } };
  assert.equal(failureAction({ status: 400, body: outageLooking, state, dropped: [] }).action, "notice");
  for (const status of [401, 403, 404, 405, 408, 409, 413, 422, 429, 499]) {
    const decision = failureAction({ status, body: envelope("sort"), state, dropped: [] });
    assert.equal(decision.action, "notice", `status ${status} gave ${decision.action}`);
  }
});

test("hostile field names leave Object.prototype and the state's keys alone", () => {
  load(ALL_BAD);
  const names = ["__proto__", "constructor", "prototype", "toString", "valueOf", "hasOwnProperty"];
  for (const name of names) {
    const next = withoutField(state, name);
    failureAction({ status: 400, body: JSON.parse(`{"error":{"field":"${name}","__proto__":{"polluted":1}}}`), state: next, dropped: [] });
    assert.deepStrictEqual(Object.keys(next).sort(), Object.keys(defaults).sort(), `${name} changed the keys`);
    assert.ok(Object.getPrototypeOf(next) === Object.prototype, `${name} changed the prototype`);
  }
  assert.equal(({}).polluted, undefined);
  assert.equal(Object.prototype.polluted, undefined);
});

function deepFreeze(value) {
  for (const inner of Object.values(value)) if (inner && typeof inner === "object") deepFreeze(inner);
  return Object.freeze(value);
}

test("withoutField works on a deeply frozen state and never shares a list with it or with the defaults", () => {
  load(ALL_BAD);
  const frozen = deepFreeze({ ...state, types: [...state.types] });
  for (const field of [...ADDRESS_FIELDS, "page", "nonsense"]) {
    const next = withoutField(frozen, field);
    assert.ok(next !== frozen, `${field}: same object`);
    assert.ok(next.types !== frozen.types, `${field}: types shared with the input`);
    assert.ok(next.types !== defaults.types, `${field}: types shared with the defaults`);
    next.types.push("Fire");
    assert.deepStrictEqual(defaults.types, [], `${field}: pushing on the copy reached the defaults`);
  }
  assert.deepStrictEqual(withoutField(frozen, "nonsense"), { ...frozen, types: [...frozen.types] });
});

test("dropping sort keeps order, which the server accepts on its own", () => {
  // The server validates order by itself and only applies it to hp and name
  // sorts, so an order left behind by a dropped sort is valid and ignored; it
  // cannot draw a second 400. Dropping order likewise keeps a valid sort.
  load("?q=pika&sort=bogus&order=asc");
  const next = withoutField(state, "sort");
  assert.equal(next.sort, "");
  assert.equal(next.order, "asc");
  load("?q=pika&sort=hp&order=sideways");
  assert.equal(withoutField(state, "order").sort, "hp");
});

// ignoredNotice

function hasLoneSurrogate(text) {
  for (let i = 0; i < text.length; i++) {
    const code = text.charCodeAt(i);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = text.charCodeAt(i + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return true;
      i++;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true;
    }
  }
  return false;
}

test("ignoredNotice never cuts an emoji in half when it shortens a value", () => {
  // Every offset, so one of them puts a surrogate pair across the cut.
  for (let pad = 0; pad < 4; pad++) {
    const value = `${"a".repeat(pad)}${"⚡".repeat(5)}${"🔥".repeat(40)}`;
    const notice = ignoredNotice([{ field: "set", value }]);
    assert.ok(!hasLoneSurrogate(notice), `half a surrogate pair left at pad ${pad}: ${JSON.stringify(notice.slice(-12))}`);
    assert.ok(notice.includes("…"), `pad ${pad}: not shortened`);
  }
});

test("ignoredNotice quotes hostile values as plain text and caps a huge one", () => {
  const hostile = ['"><img src=x onerror=alert(1)>', "a\nb", "‮evil", "x&amp;y"];
  for (const value of hostile) {
    assert.equal(ignoredNotice([{ field: "rarity", value }]), `Ignored "rarity=${value}": not a valid value.`);
  }
  const huge = ignoredNotice([{ field: "set", value: "z".repeat(10000) }]);
  assert.ok(huge.length < 120, `notice is ${huge.length} characters`);
  assert.ok(huge.includes(`"set=${"z".repeat(40)}…"`));
  // A value exactly at the limit is shown whole; one over is shortened.
  assert.ok(!ignoredNotice([{ field: "set", value: "z".repeat(40) }]).includes("…"));
  assert.ok(ignoredNotice([{ field: "set", value: "z".repeat(41) }]).includes("…"));
});

test("ignoredNotice lists any number of fields as one sentence with a single 'and'", () => {
  for (const count of [1, 2, 3, 8]) {
    const dropped = Array.from({ length: count }, (_, i) => ({ field: `f${i}`, value: `v${i}` }));
    const notice = ignoredNotice(dropped);
    assert.ok(notice.startsWith("Ignored "), notice);
    assert.equal(notice.split(" and ").length, count === 1 ? 1 : 2, notice);
    assert.ok(notice.endsWith(count === 1 ? ": not a valid value." : ": not valid values."), notice);
    for (let i = 0; i < count; i++) assert.ok(notice.includes(`"f${i}=v${i}"`), notice);
  }
});

test("the notice reaches the page as text, never as markup", () => {
  const api = readFileSync(new URL("./api.js", import.meta.url), "utf8");
  const show = api.slice(api.indexOf("export function showNotice"), api.indexOf("export function hideNotice"));
  assert.match(show, /\$\("search-notice-text"\)\.textContent = text;/);
  for (const name of readdirSync(new URL(".", import.meta.url)).filter((file) => file.endsWith(".js"))) {
    const source = readFileSync(new URL(`./${name}`, import.meta.url), "utf8");
    for (const sink of [".innerHTML", "insertAdjacentHTML", ".outerHTML", "document.write"]) {
      assert.ok(!source.includes(sink), `${name} uses ${sink}`);
    }
  }
});

// historyAction as a state machine. The model mirrors api.js: writeHistory for
// a search a trigger starts, commitQuery for Enter, blur and idle, and the
// draft flag each of them keeps.

function historyModel() {
  const stack = [""];
  let index = 0;
  let draft = false;
  let query = "";
  const changed = () => query !== stack[index];
  function write(trigger) {
    const action = historyAction(trigger, { draft, changed: changed() });
    if (action === "none") return;
    let pushed = false;
    if (action === "push" && changed()) {
      stack.splice(index + 1, Infinity, query);
      index++;
      pushed = true;
    } else {
      stack[index] = query;
    }
    if (trigger === "typing") {
      if (pushed) draft = true;
    } else if (trigger === "enter" || trigger === "blur" || trigger === "idle" || pushed) {
      draft = false;
    }
  }
  return {
    stack,
    get index() {
      return index;
    },
    type(text) {
      query = text;
      write("typing");
    },
    commit(trigger) {
      if (historyAction(trigger, { draft, changed: changed() }) === "none") {
        draft = false;
        return;
      }
      write(trigger);
    },
    filter(tag) {
      query = `${query}|${tag}`;
      write("filter");
    },
    back() {
      if (index > 0) index--;
      query = stack[index];
      draft = false;
      if (historyAction("popstate", { draft, changed: false }) !== "none") throw new Error("popstate wrote history");
    },
  };
}

test("any mix of commits after one typed word leaves exactly one entry for it", () => {
  const commits = ["enter", "blur", "idle"];
  const sequences = [[]];
  for (let length = 1; length <= 4; length++) {
    for (const prefix of sequences.filter((sequence) => sequence.length === length - 1)) {
      for (const trigger of commits) sequences.push([...prefix, trigger]);
    }
  }
  for (const sequence of sequences) {
    const model = historyModel();
    for (const prefix of ["c", "ch", "cha", "char", "chari", "charizard"]) model.type(prefix);
    for (const trigger of sequence) model.commit(trigger);
    assert.deepStrictEqual(model.stack, ["", "charizard"], `after ${sequence.join(",") || "nothing"}`);
    model.back();
    assert.equal(model.index, 0, "Back returns to before the typing");
  }
});

test("a commit in the middle of a burst splits it into exactly two entries", () => {
  for (const trigger of ["enter", "blur", "idle"]) {
    const model = historyModel();
    model.type("char");
    model.commit(trigger);
    model.type("chariz");
    model.type("charizard");
    model.commit(trigger);
    assert.deepStrictEqual(model.stack, ["", "char", "charizard"], trigger);
  }
});

test("a typed query followed by a filter click keeps its own entry", () => {
  // The click blurs the box first; without the blur the filter must still not
  // overwrite the typed query.
  for (const blurFirst of [true, false]) {
    const model = historyModel();
    model.type("pika");
    model.type("pikachu");
    if (blurFirst) model.commit("blur");
    model.filter("fire");
    assert.deepStrictEqual(model.stack, ["", "pikachu", "pikachu|fire"], `blur first: ${blurFirst}`);
    model.back();
    assert.equal(model.stack[model.index], "pikachu");
  }
});

test("typing after Back starts a fresh entry instead of rewriting the one Back landed on", () => {
  const model = historyModel();
  model.type("pikachu");
  model.commit("idle");
  model.type("raichu");
  model.commit("idle");
  model.back();
  assert.equal(model.stack[model.index], "pikachu");
  model.type("pikachu v");
  assert.deepStrictEqual(model.stack, ["", "pikachu", "pikachu v"]);
});

test("triggers it does not know never push, and popstate never writes", () => {
  for (const trigger of ["", "refresh", "retry", "TYPING", "Enter", undefined, null, "__proto__", "constructor"]) {
    for (const draft of [true, false]) {
      for (const changed of [true, false]) {
        assert.notEqual(historyAction(trigger, { draft, changed }), "push", `${String(trigger)} pushed`);
      }
    }
  }
  for (const draft of [true, false]) {
    for (const changed of [true, false]) assert.equal(historyAction("popstate", { draft, changed }), "none");
  }
});
