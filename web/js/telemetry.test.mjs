import test from "node:test";
import assert from "node:assert/strict";
import { quantile } from "./telemetry.js";

test("quantile of an empty sample is 0 at any p", () => {
  assert.equal(quantile([], 0), 0);
  assert.equal(quantile([], 0.5), 0);
  assert.equal(quantile([], 1), 0);
});

test("quantile of one sample is that sample at any p the page uses", () => {
  for (const p of [0.5, 0.95, 1]) {
    assert.equal(quantile([42], p), 42);
  }
});

test("quantile at p=1 is the largest", () => {
  assert.equal(quantile([3, 5, 8, 13, 21], 1), 21);
});

test("quantile at p=0 reads index -1 and returns undefined", () => {
  // Pinned as it behaves today. The page only asks for 0.5 and 0.95, so
  // nothing reaches this, but the result is not the smallest sample.
  assert.equal(quantile([3, 5, 8], 0), undefined);
  assert.equal(quantile([42], 0), undefined);
});

test("quantile uses the nearest rank", () => {
  const sorted = [10, 20, 30, 40];
  assert.equal(quantile(sorted, 0.25), 10);
  assert.equal(quantile(sorted, 0.26), 20);
  assert.equal(quantile(sorted, 0.5), 20);
  assert.equal(quantile(sorted, 0.51), 30);
  assert.equal(quantile(sorted, 0.75), 30);
  assert.equal(quantile(sorted, 0.76), 40);
});

test("quantile p95 of twenty samples is the 19th, and p96 the 20th", () => {
  const sorted = Array.from({ length: 20 }, (_, i) => i + 1);
  assert.equal(quantile(sorted, 0.95), 19);
  assert.equal(quantile(sorted, 0.96), 20);
  // Fewer than twenty samples: the honest reading is the maximum.
  assert.equal(quantile([1, 2, 3, 4, 5], 0.95), 5);
});

test("quantile trusts its caller to sort: an unsorted input is read by position", () => {
  // The function does not sort. Callers hand it a sorted copy; this pins that
  // contract so nobody assumes it sorts for them.
  assert.equal(quantile([30, 10, 20], 1), 20);
  assert.equal(quantile([30, 10, 20], 0.01), 30);
  const sorted = [30, 10, 20].sort((a, b) => a - b);
  assert.equal(quantile(sorted, 1), 30);
  assert.equal(quantile(sorted, 0.01), 10);
});

test("quantile does not mutate its input", () => {
  const sample = [1, 2, 3];
  quantile(sample, 0.5);
  assert.deepStrictEqual(sample, [1, 2, 3]);
});
