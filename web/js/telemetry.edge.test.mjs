import test from "node:test";
import assert from "node:assert/strict";
import { quantile } from "./telemetry.js";

// The latency readout asks for p50 and p95 over at most twenty samples, but
// the nearest rank must be right for any sample size: ceil(p * n) in exact
// arithmetic, where float rounding of p * n could push it one rank up.
test("quantile is the exact nearest rank at the percentiles the page uses, for every sample size", () => {
  for (const pct of [50, 95]) {
    for (let n = 1; n <= 2000; n++) {
      const sample = Array.from({ length: n }, (_, i) => i);
      const rank = Math.ceil((pct * n) / 100);
      assert.equal(quantile(sample, pct / 100), rank - 1, `p${pct} of ${n}`);
    }
  }
});

test("quantile never reads past the end, whatever p is above 1", () => {
  const sample = [1, 2, 3];
  for (const p of [1, 1.0000001, 2, 1e9, Infinity]) {
    assert.equal(quantile(sample, p), 3, `p ${p}`);
  }
});

test("quantile of one sample is that sample at every p from just above 0", () => {
  for (const p of [Number.MIN_VALUE, 0.001, 0.5, 0.95, 1, 7]) {
    assert.equal(quantile([42], p), 42, `p ${p}`);
  }
});

test("quantile is monotonic in p on a sorted sample", () => {
  const sample = [3, 5, 8, 13, 21, 34, 55];
  let last = -Infinity;
  for (let p = 0.01; p <= 1.0001; p += 0.01) {
    const got = quantile(sample, p);
    assert.ok(got >= last, `p ${p.toFixed(2)} gave ${got} after ${last}`);
    last = got;
  }
});

test("quantile returns a member of the sample and leaves duplicates and negatives alone", () => {
  assert.equal(quantile([5, 5, 5, 5], 0.5), 5);
  assert.equal(quantile([-3, -2, -1], 0.5), -2);
  assert.equal(quantile([0, 0, 0, 100], 0.95), 100);
  assert.equal(quantile([0, 0, 0, 100], 0.75), 0);
});
