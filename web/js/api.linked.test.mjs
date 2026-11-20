import test from "node:test";
import assert from "node:assert/strict";
import { fetchLinkedCard } from "./api.js";

// fetchLinkedCard asks for one card by id and hands back what the deep link
// needs to judge the outcome. fetch is replaced by a plain function per case.

function answering(respond) {
  const seen = [];
  globalThis.fetch = async (url) => {
    seen.push(url);
    return respond();
  };
  return seen;
}

const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

test("a found card comes back with its status, asked for by id on the search route", async () => {
  const card = { id: "ex10-?", name: "Unown" };
  const seen = answering(() => json({ results: [card] }));
  const outcome = await fetchLinkedCard("ex10-?");
  assert.equal(seen[0], "/api/search?id=ex10-%3F");
  assert.equal(outcome.status, 200);
  assert.equal(outcome.networkError, false);
  assert.ok(outcome.card === card || outcome.card.id === "ex10-?", "the card is the first result");
});

test("an empty result is a 200 with no card", async () => {
  answering(() => json({ results: [] }));
  assert.deepStrictEqual(await fetchLinkedCard("nope-999"), { status: 200, card: null, networkError: false });
});

test("an error status comes back as that status with no card", async () => {
  for (const status of [400, 404, 408, 429, 500, 503]) {
    answering(() => json({ error: { code: "x" } }, status));
    assert.deepStrictEqual(await fetchLinkedCard("base1-4"), { status, card: null, networkError: false }, String(status));
  }
});

test("a request that never got an answer is a network failure", async () => {
  globalThis.fetch = async () => { throw new TypeError("Failed to fetch"); };
  assert.deepStrictEqual(await fetchLinkedCard("base1-4"), { status: 0, card: null, networkError: true });
});

test("an answer whose body is not JSON counts as a network failure", async () => {
  answering(() => new Response("<html>gateway</html>", { status: 200 }));
  const outcome = await fetchLinkedCard("base1-4");
  assert.equal(outcome.networkError, true);
  assert.equal(outcome.card, null);
});
