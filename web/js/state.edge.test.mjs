import test from "node:test";
import assert from "node:assert/strict";
import {
  state,
  defaults,
  readStateFromURL,
  buildParams,
  effectiveSort,
  effectiveOrder,
} from "./state.js";

function load(search = "") {
  globalThis.location = { search, pathname: "/", hash: "" };
  readStateFromURL();
}

test.beforeEach(() => load(""));
test.after(() => {
  delete globalThis.location;
});

// The keys the server understands from the page. Anything else in a rebuilt
// querystring would be a parameter the page invented.
const SENT = new Set(["q", "supertype", "types", "set", "rarity", "series", "sort", "order", "page", "debug"]);

// Addresses a person, a bookmark or a hostile link can put in the bar.
const HOSTILE = [
  "?q=first&q=second&q=third",
  "?q=&types=&set=&rarity=&series=&sort=&order=",
  "?q&types&set",
  "?q=a+b%2Bc%20d",
  "?types=Fire%252CWater",
  "?types=Fire%2CWater%2C%2C",
  "?types=%2C%2C%2C",
  "?types=%20Fire%20,%20Water%20",
  "?types=Fire&types=Water",
  "?page=abc&page_size=-5&debug=yes&q=mew",
  "?page=2&page_size=999999999999",
  "?unknown=1&__proto__=x&constructor=y&q=mew",
  "?q=%E0%A4%A",
  "?q=%",
  "?q=%00%01%1F",
  "?q=%F0%9F%98%80%E2%80%8D%F0%9F%94%A5",
  "?q=%20%20%20",
  "?sort=%20hp%20&order=%20asc",
  `?q=${"x".repeat(100000)}`,
  `?types=${"Fire,".repeat(20000)}`,
];

// A stored or shared address is read, rewritten by the page, and read again.
// Whatever the first rewrite does to a hostile address, the second must agree
// with it, or the bar would keep changing under the user's hands.
test("rewriting the address is stable: what buildParams writes, readStateFromURL reads back and buildParams writes again", () => {
  for (const search of HOSTILE) {
    load(search);
    const first = buildParams().toString();
    load(first ? `?${first}` : "");
    const second = buildParams().toString();
    assert.equal(second, first, `unstable for ${search.slice(0, 60)}`);
  }
});

test("a rewritten address only carries parameters the server knows, each once, and never page or debug unasked", () => {
  for (const search of HOSTILE) {
    load(search);
    const sp = buildParams();
    const keys = [...sp.keys()];
    assert.equal(new Set(keys).size, keys.length, `repeated key for ${search.slice(0, 60)}`);
    for (const key of keys) assert.ok(SENT.has(key), `unexpected key ${key}`);
    assert.ok(!sp.has("page") && !sp.has("debug"), "page and debug are not part of the address");
    assert.ok(!sp.has("page_size"), "the page never sends a page size");
  }
});

test("reading a hostile address never throws and always leaves the state a well-formed value", () => {
  for (const search of HOSTILE) {
    load(search);
    for (const key of ["q", "supertype", "set", "rarity", "series", "sort", "order"]) {
      assert.equal(typeof state[key], "string", `${key} for ${search.slice(0, 60)}`);
    }
    assert.ok(Array.isArray(state.types));
    assert.ok(state.types.every((t) => typeof t === "string" && t !== ""));
    assert.equal(state.page, 1);
    assert.deepStrictEqual(Object.keys(state).sort(), Object.keys(defaults).sort());
  }
});

// An encoded comma is a separator once decoded, so a member can never contain
// a literal comma; a doubly encoded one decodes to text that keeps its percent
// sign and goes back out encoded once more.
test("types: a percent-encoded comma separates, a double-encoded one stays inside its member", () => {
  load("?types=Fire%2CWater");
  assert.deepStrictEqual(state.types, ["Fire", "Water"]);
  assert.equal(buildParams().get("types"), "Fire,Water");

  load("?types=Fire%252CWater");
  assert.deepStrictEqual(state.types, ["Fire%2CWater"]);
  const out = buildParams().toString();
  assert.equal(out, "types=Fire%252CWater");
  load(`?${out}`);
  assert.deepStrictEqual(state.types, ["Fire%2CWater"]);
});

// A plus is a space in a querystring and a literal plus only when encoded; the
// page must keep the two apart in both directions.
test("a plus and %20 both read as a space, and an encoded plus stays a plus through a rewrite", () => {
  load("?q=a+b");
  const plus = state.q;
  load("?q=a%20b");
  assert.equal(state.q, plus);
  assert.equal(state.q, "a b");

  load("?q=c%2B%2B");
  assert.equal(state.q, "c++");
  const out = buildParams().toString();
  assert.equal(out, "q=c%2B%2B");
  load(`?${out}`);
  assert.equal(state.q, "c++");
});

test("the rewritten query is trimmed, so reading it back differs from the first read only by that trim", () => {
  load("?q=%20%20pika%20chu%20%20");
  assert.equal(state.q, "  pika chu  ");
  load(`?${buildParams()}`);
  assert.equal(state.q, "pika chu");
});

test("a very long query survives the round trip byte for byte", () => {
  const q = "ü".repeat(50000);
  load(`?q=${encodeURIComponent(q)}`);
  assert.equal(state.q.length, 50000);
  load(`?${buildParams()}`);
  assert.equal(state.q, q);
});

test("a number-like or hostile page in the address is not state, and a bad page in the state is not sent", () => {
  load("?page=7&page_size=100");
  assert.equal(state.page, 1);
  for (const page of [0, -1, NaN, undefined, null]) {
    state.page = page;
    assert.equal(buildParams({ page: true }).has("page"), false, `page ${page}`);
  }
  state.page = 2;
  assert.equal(buildParams({ page: true }).get("page"), "2");
});

test("parameter names are case-sensitive: Q and TYPES are not q and types", () => {
  load("?Q=mew&TYPES=Fire&Sort=hp");
  assert.deepStrictEqual(state, defaults);
});

test("effectiveOrder follows the effective sort, not the raw one", () => {
  // relevance with no query is really newest, which has no default direction.
  load("?sort=relevance");
  assert.equal(effectiveSort(), "newest");
  assert.equal(effectiveOrder(), "");
  // A sort the server does not know is passed through and gets no default order.
  load("?sort=HP");
  assert.equal(effectiveSort(), "HP");
  assert.equal(effectiveOrder(), "");
  // An explicit order is never second-guessed, even a meaningless one.
  load("?sort=hp&order=sideways");
  assert.equal(effectiveOrder(), "sideways");
});

// Each case starts from the same place whatever ran before it, so a suite that
// passes in file order also passes in any other order.
test("the shared state is reset by an empty read, whatever a previous case left in it", () => {
  load("?q=mew&types=Fire&sort=hp&order=asc");
  state.page = 9;
  state.types.push("Water");
  load("");
  assert.deepStrictEqual(state, defaults);
  assert.deepStrictEqual(defaults.types, []);
  assert.deepStrictEqual(defaults, {
    q: "", supertype: "", types: [], set: "", rarity: "", series: "", sort: "", order: "", page: 1,
  });
});
