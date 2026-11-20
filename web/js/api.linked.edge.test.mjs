import test from "node:test";
import assert from "node:assert/strict";
import { fetchLinkedCard } from "./api.js";
import { deepLinkFailure } from "./modal.js";

// fetchLinkedCard against every kind of answer, judged the way the dialog
// judges it. fetch is a plain function per case.

const NOT_FOUND = "That card isn't in the archive.";
const BAD_LINK = "That link does not point to a PokéSearch card.";
const UNAVAILABLE = "PokéSearch could not load that card. Reload the page to try again.";

const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const card = { id: "base1-4", name: "Charizard" };

// [name, how fetch behaves, the message the dialog shows (null: it opens)]
const CASES = [
  ["200 with the card", async () => json({ results: [card] }), null],
  ["200 with no result", async () => json({ results: [] }), NOT_FOUND],
  ["200 with results missing", async () => json({}), NOT_FOUND],
  ["200 with results null", async () => json({ results: null }), NOT_FOUND],
  ["200 with a null first result", async () => json({ results: [null] }), NOT_FOUND],
  ["200 with a JSON null body", async () => json(null), UNAVAILABLE],
  ["200 with a body that is not JSON", async () => new Response("<html>gateway</html>", { status: 200 }), UNAVAILABLE],
  ["200 with an empty body", async () => new Response("", { status: 200 }), UNAVAILABLE],
  ["204", async () => new Response(null, { status: 204 }), UNAVAILABLE],
  ["a response whose json() rejects", async () => ({ ok: true, status: 200, json: () => Promise.reject(new SyntaxError("bad")) }), UNAVAILABLE],
  ["400", async () => json({ error: { code: "invalid_param" } }, 400), BAD_LINK],
  ["404", async () => json({ error: { code: "not_found" } }, 404), NOT_FOUND],
  ["404 with a body that is not JSON", async () => new Response("nope", { status: 404 }), NOT_FOUND],
  ["408", async () => new Response("", { status: 408 }), UNAVAILABLE],
  ["429", async () => new Response("", { status: 429 }), UNAVAILABLE],
  ["500", async () => new Response("", { status: 500 }), UNAVAILABLE],
  ["503", async () => json({ error: { code: "es_unavailable" } }, 503), UNAVAILABLE],
  ["a rejected fetch", async () => { throw new TypeError("Failed to fetch"); }, UNAVAILABLE],
  ["a fetch that throws before it returns a promise", () => { throw new TypeError("boom"); }, UNAVAILABLE],
  ["an aborted fetch", async () => { throw new DOMException("The operation was aborted.", "AbortError"); }, UNAVAILABLE],
];

test("fetchLinkedCard never throws, and every answer leads the dialog to the right verdict", async () => {
  for (const [name, respond, message] of CASES) {
    globalThis.fetch = respond;
    let outcome;
    await assert.doesNotReject(async () => { outcome = await fetchLinkedCard("base1-4"); }, name);
    assert.equal(typeof outcome.status, "number", name);
    assert.equal(typeof outcome.networkError, "boolean", name);
    const failure = deepLinkFailure({ id: "base1-4", status: outcome.status, found: outcome.card !== null, networkError: outcome.networkError });
    assert.equal(failure === null ? null : failure.message, message, name);
    if (message === null) assert.ok(outcome.card === card || outcome.card.id === card.id, name);
    if (message === UNAVAILABLE) assert.equal(failure.clearHash, false, `${name}: the hash stays for a reload`);
  }
});

test("the id travels as the one id parameter, however it has to be encoded", async () => {
  const ids = ["ex10-?", "ex10-!", "cel25c-15_A1", "a&b=c", "a#b", "a b", "a+b", "a/b", "é", "a%20b", "x".repeat(200)];
  for (const id of ids) {
    const seen = [];
    globalThis.fetch = async (url) => {
      seen.push(url);
      return json({ results: [] });
    };
    await fetchLinkedCard(id);
    assert.equal(seen.length, 1, id);
    const url = new URL(seen[0], "http://127.0.0.1");
    assert.equal(url.pathname, "/api/search", id);
    assert.equal(url.hash, "", `${id}: nothing leaks into a fragment`);
    assert.deepStrictEqual([...url.searchParams.keys()], ["id"], `${id}: one parameter`);
    assert.equal(url.searchParams.get("id"), id, id);
  }
});

test("a failed request is reported once and leaves no state behind for the next one", async () => {
  globalThis.fetch = async () => { throw new TypeError("Failed to fetch"); };
  assert.deepStrictEqual(await fetchLinkedCard("base1-4"), { status: 0, card: null, networkError: true });
  globalThis.fetch = async () => json({ results: [card] });
  const outcome = await fetchLinkedCard("base1-4");
  assert.equal(outcome.networkError, false);
  assert.equal(outcome.status, 200);
  assert.ok(outcome.card !== null);
});
