import test from "node:test";
import assert from "node:assert/strict";
import { pillStatus } from "./telemetry.js";

const ONLINE = { label: "Online", degraded: false };
const DEGRADED = { label: "Degraded", degraded: true };

test("nothing known yet reads Connecting", () => {
  assert.deepStrictEqual(pillStatus({ search: null, health: null }), { label: "Connecting", degraded: false });
});

test("a health result alone decides the pill", () => {
  assert.deepStrictEqual(pillStatus({ search: null, health: { at: 1, ok: true } }), ONLINE);
  assert.deepStrictEqual(pillStatus({ search: null, health: { at: 1, ok: false } }), DEGRADED);
});

test("a search outcome alone decides the pill", () => {
  assert.deepStrictEqual(pillStatus({ search: { at: 1, ok: true }, health: null }), ONLINE);
  assert.deepStrictEqual(pillStatus({ search: { at: 1, ok: false }, health: null }), DEGRADED);
});

test("a health check started before the search finished does not overwrite it", () => {
  // Health started at 1, the search finished at 2: the search owns the pill.
  assert.deepStrictEqual(pillStatus({ search: { at: 2, ok: true }, health: { at: 1, ok: false } }), ONLINE);
  assert.deepStrictEqual(pillStatus({ search: { at: 2, ok: false }, health: { at: 1, ok: true } }), DEGRADED);
});

test("a health check started after the search finished is newer", () => {
  assert.deepStrictEqual(pillStatus({ search: { at: 1, ok: true }, health: { at: 2, ok: false } }), DEGRADED);
  assert.deepStrictEqual(pillStatus({ search: { at: 1, ok: true }, health: { at: 2, ok: true } }), ONLINE);
});

test("the pill never says Online while the newest search is an outage", () => {
  for (const at of [0, 1, 2, 3]) {
    assert.deepStrictEqual(pillStatus({ search: { at: 2, ok: false }, health: { at, ok: true } }), DEGRADED, `health at ${at}`);
  }
});

test("the order the two answers arrive in does not matter, only when each was taken", () => {
  const search = { at: 5, ok: false };
  const health = { at: 3, ok: true };
  assert.deepStrictEqual(pillStatus({ search, health }), DEGRADED);
  assert.deepStrictEqual(pillStatus({ health, search }), DEGRADED);
});
