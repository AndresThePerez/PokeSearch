import test from "node:test";
import assert from "node:assert/strict";
import { pillStatus } from "./telemetry.js";

const ONLINE = { label: "Online", degraded: false };
const DEGRADED = { label: "Degraded", degraded: true };

// Two searches and two health checks, each with a start and a finish. A search
// outcome is taken when it finishes, a health result when its check started;
// the pill keeps the newest outcome of each kind, as telemetry.js does.
function permutations(items) {
  if (items.length <= 1) return [items];
  return items.flatMap((item, i) => permutations([...items.slice(0, i), ...items.slice(i + 1)]).map((rest) => [item, ...rest]));
}

test("no interleaving of two searches and two health checks puts Online under an outage", () => {
  const events = ["s1-start", "s1-end", "s2-start", "s2-end", "h1-start", "h1-end", "h2-start", "h2-end"];
  for (const outcomes of [[true, true], [true, false], [false, true], [false, false]]) {
    for (const healthOk of [[true, true], [true, false], [false, true], [false, false]]) {
      for (const order of permutations(events)) {
        // Each operation must start before it ends, and searches start in order.
        const at = Object.fromEntries(order.map((event, i) => [event, i + 1]));
        if (at["s1-end"] < at["s1-start"] || at["s2-end"] < at["s2-start"]) continue;
        if (at["h1-end"] < at["h1-start"] || at["h2-end"] < at["h2-start"]) continue;
        if (at["s2-start"] < at["s1-start"]) continue;
        let search = null;
        let health = null;
        for (const event of order) {
          const [who, phase] = event.split("-");
          if (phase !== "end") continue;
          if (who === "s1" || who === "s2") {
            const ok = outcomes[who === "s1" ? 0 : 1];
            search = { at: at[event], ok };
          } else {
            const started = at[`${who}-start`];
            const ok = healthOk[who === "h1" ? 0 : 1];
            if (!health || started > health.at) health = { at: started, ok };
          }
          const pill = pillStatus({ search, health });
          if (search && !search.ok) {
            assert.deepStrictEqual(pill, DEGRADED, `${order.join(" ")} at ${event}`);
          }
          if (search && health && health.at < search.at) {
            // A check started before the newest search finished does not decide.
            assert.deepStrictEqual(pill, search.ok ? ONLINE : DEGRADED, `${order.join(" ")} at ${event}`);
          }
        }
      }
    }
  }
});

test("equal timestamps go to the search, every time", () => {
  for (const searchOk of [true, false]) {
    for (const healthOk of [true, false]) {
      const first = pillStatus({ search: { at: 3, ok: searchOk }, health: { at: 3, ok: healthOk } });
      const second = pillStatus({ health: { at: 3, ok: healthOk }, search: { at: 3, ok: searchOk } });
      assert.deepStrictEqual(first, searchOk ? ONLINE : DEGRADED);
      assert.deepStrictEqual(second, first);
    }
  }
});

test("missing, NaN and odd times never throw and always give a known label", () => {
  const times = [undefined, null, NaN, Infinity, -Infinity, 0, -1, "2"];
  const labels = new Set(["Online", "Degraded", "Connecting"]);
  for (const s of times) {
    for (const h of times) {
      for (const searchOk of [true, false]) {
        for (const healthOk of [true, false]) {
          const pill = pillStatus({ search: { at: s, ok: searchOk }, health: { at: h, ok: healthOk } });
          assert.ok(labels.has(pill.label), `${s}/${h}: ${pill.label}`);
          if (!searchOk) assert.deepStrictEqual(pill, DEGRADED, `${s}/${h}: outage not shown`);
        }
      }
    }
  }
  assert.deepStrictEqual(pillStatus({ search: undefined, health: undefined }), { label: "Connecting", degraded: false });
});

test("a healthy check after a good search reads Online, an unhealthy one after it reads Degraded", () => {
  assert.deepStrictEqual(pillStatus({ search: { at: 1, ok: true }, health: { at: 2, ok: true } }), ONLINE);
  assert.deepStrictEqual(pillStatus({ search: { at: 1, ok: true }, health: { at: 2, ok: false } }), DEGRADED);
  // After an outage the banner stays until a search succeeds, so a healthy
  // check alone keeps the pill in agreement with it.
  assert.deepStrictEqual(pillStatus({ search: { at: 1, ok: false }, health: { at: 2, ok: true } }), DEGRADED);
});
