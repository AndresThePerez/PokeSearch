// The observability rail: the stats grid, the SLA readouts, the service-status
// pill, and the two inspector panes that show the generated DSL and the raw
// response. Imports util only.

import { $ } from "./util.js";

let hadDSL = false;

// debugEnabled gates the debug=1 request parameter on whether anyone is
// actually looking. The DSL is the largest part of a search response and it
// used to be requested on every keystroke, open panel or not.
export function debugEnabled() {
  return $("query-inspector").open;
}

// lastResponseHadDSL lets main.js decide whether opening the panel needs a
// re-fetch or whether the DSL is already on screen.
export function lastResponseHadDSL() {
  return hadDSL;
}

export function renderInspector(dsl, response) {
  hadDSL = Boolean(dsl);
  $("dsl-json").textContent = dsl
    ? JSON.stringify(dsl, null, 2)
    : "Open this panel to capture the query — the next search will return its DSL.";
  $("response-json").textContent = response ? JSON.stringify(response, null, 2) : "No response available.";
}

// The placeholder the markup ships with: a readout with nothing to say.
const BLANK = "—";

export function setDegraded(isDegraded) {
  $("degraded-banner").hidden = !isDegraded;
  setServiceStatus(isDegraded ? "Degraded" : "Online", isDegraded);
  // A failed search leaves the previous query's totals on screen, where they
  // read as a count of what is showing now. Blank them to the same dash the
  // page starts with — the elements and their live regions stay put, so the
  // next success has somewhere to land and the change is still announced.
  if (isDegraded) {
    $("total-count").textContent = BLANK;
    $("stat-results").textContent = BLANK;
    $("stat-roundtrip").textContent = BLANK;
  }
}

export function setServiceStatus(label, degraded = false) {
  $("service-status").lastChild.textContent = label;
  $("service-status").classList.toggle("is-degraded", degraded);
}

// The session's latencies, oldest first. Twenty is a screenful of sparkline at
// the rail's width, and about as far back as "just now" reaches for a person
// watching their own searches.
const HISTORY_LIMIT = 20;
const history = [];

// The sparkline's own coordinate space. preserveAspectRatio="none" stretches
// it to whatever width the rail has, so these are units, not pixels.
const SPARK_W = 200;
const SPARK_H = 40;
const SPARK_TOP = 4;

// recordTiming is the rail's memory: one entry per completed search, feeding
// the waterfall (where did this request's milliseconds go), the sparkline
// (what has the session looked like) and the request-id line (which log line
// is this). All three come from data the rail already receives — no extra
// request is made to draw any of them.
export function recordTiming(esMs, totalMs, requestId) {
  const es = Number.isFinite(esMs) ? esMs : 0;
  const total = Number.isFinite(totalMs) ? Math.round(totalMs) : 0;
  history.push({ es, total });
  if (history.length > HISTORY_LIMIT) history.shift();
  renderWaterfall(es, total);
  renderSparkline();
  $("stat-request-id").textContent = requestId || "—";
}

// The waterfall answers "was that Elasticsearch, or was that us?". ES time is
// what the server measured; everything else — network, handler, JSON, render —
// is the remainder, which is exactly the half a server-side number cannot see.
function renderWaterfall(es, total) {
  const rest = Math.max(total - es, 0);
  // A zero-width flex child would vanish; the tiny floor keeps both segments
  // visible so the bar always reads as a comparison of two things.
  $("waterfall-es").style.flexGrow = String(es || 0.001);
  $("waterfall-rest").style.flexGrow = String(rest || 0.001);
  $("wf-es-ms").textContent = `${es} ms`;
  $("wf-rest-ms").textContent = `${Math.round(rest)} ms`;
  $("latency-waterfall").setAttribute("aria-label",
    `Latency breakdown: Elasticsearch ${es} milliseconds, everything else ${Math.round(rest)} milliseconds`);
}

// quantile picks the nearest-rank value: with 20 samples p95 is the highest,
// which is the honest reading of a sample this small.
function quantile(sorted, p) {
  return sorted.length ? sorted[Math.min(sorted.length - 1, Math.ceil(p * sorted.length) - 1)] : 0;
}

function renderSparkline() {
  const svg = $("latency-spark");
  const max = Math.max(...history.map((entry) => entry.total), 1);
  const step = SPARK_W / Math.max(history.length - 1, 1);
  const y = (entry) => (SPARK_H - 2 - (entry.total / max) * (SPARK_H - 2 - SPARK_TOP)).toFixed(1);
  // One search is a point, and a polyline through one point draws nothing —
  // so the first search reads as a flat line at its own value instead.
  const points = history.length === 1
    ? `0,${y(history[0])} ${SPARK_W},${y(history[0])}`
    : history.map((entry, index) => `${(index * step).toFixed(1)},${y(entry)}`).join(" ");
  const line = document.createElementNS("http://www.w3.org/2000/svg", "polyline");
  line.setAttribute("points", points);
  line.setAttribute("class", "spark-line");
  svg.replaceChildren(line);

  const sorted = history.map((entry) => entry.total).sort((a, b) => a - b);
  const p50 = Math.round(quantile(sorted, 0.5));
  const p95 = Math.round(quantile(sorted, 0.95));
  $("spark-p50").textContent = `${p50} ms`;
  $("spark-p95").textContent = `${p95} ms`;
  svg.setAttribute("aria-label",
    `Round-trip latency over the last ${history.length} ${history.length === 1 ? "search" : "searches"}: median ${p50} milliseconds, 95th percentile ${p95} milliseconds`);
}

export function renderStats(data, roundTripMs) {
  $("stat-results").textContent = Number(data.total).toLocaleString();
  $("stat-engine").textContent = `${data.took_ms ?? "—"} ms`;
  $("stat-roundtrip").textContent = `${Math.round(roundTripMs)} ms`;
  // With no pages there is no page to be on: "1 / 0" is a position in a range
  // that does not exist.
  $("stat-page").textContent = data.pages ? `${data.page} / ${data.pages}` : BLANK;
  $("sla-engine-state").textContent = data.took_ms < 100 ? "within target" : "above target";
  $("sla-roundtrip-state").textContent = roundTripMs < 250 ? "within target" : "above target";
}
