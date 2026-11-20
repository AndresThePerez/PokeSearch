// The observability rail: the stats grid, the SLA readouts, the service-status
// pill, the two inspector panes that show the generated DSL and the raw
// response, and the Ranking lab below them; also the archive's counts in the
// header, read from the server. Imports util and state — state
// imports nothing app-level, so the graph stays acyclic.

import { $, element } from "./util.js";
import { queryText } from "./state.js";

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

// The order the inspector prints generated-DSL fields in: the query itself
// first, since that is what the panel exists to explain, then the clauses
// that shape the response. This is presentational only — the API response
// body is untouched, and Go still marshals its map keys in sorted order
// (which is why "aggs" precedes "query" on the wire). Any key the server
// sends that isn't listed here is appended in the order the server sent it,
// so a future addition to the DSL is never silently dropped from the panel.
const DSL_DISPLAY_ORDER = ["query", "highlight", "sort", "post_filter", "aggs"];

function orderDslForDisplay(dsl) {
  const ordered = {};
  for (const key of DSL_DISPLAY_ORDER) {
    if (Object.prototype.hasOwnProperty.call(dsl, key)) {
      ordered[key] = dsl[key];
    }
  }
  for (const key of Object.keys(dsl)) {
    if (!Object.prototype.hasOwnProperty.call(ordered, key)) {
      ordered[key] = dsl[key];
    }
  }
  return ordered;
}

export function renderInspector(dsl, response) {
  hadDSL = Boolean(dsl);
  $("dsl-json").textContent = dsl
    ? JSON.stringify(orderDslForDisplay(dsl), null, 2)
    : "Open this panel to capture the query — the next search will return its DSL.";
  $("response-json").textContent = response ? JSON.stringify(response, null, 2) : "No response available.";
}

// The placeholder the markup ships with: a readout with nothing to say.
const BLANK = "—";

// The four relevance branches, in the order the server reports them: the word
// a card's badge shows for each, and what that badge means. Keys are
// search.Branches' names, and each meaning completes "Matched:", the
// legend's heading. The card badges, their legend and the Ranking lab's
// weights all read this one table; it lives here rather than in render.js
// because render.js imports this module and not the other way round.
export const BRANCHES = Object.freeze({
  exact: Object.freeze({ label: "exact", meaning: "whole name" }),
  prefix: Object.freeze({ label: "prefix", meaning: "start of a name" }),
  "fuzzy-name": Object.freeze({ label: "fuzzy", meaning: "name, allowing a typo" }),
  text: Object.freeze({ label: "text", meaning: "card text, set or artist" }),
});

// isCount accepts what a server count can honestly be: a whole number, zero
// or more. Anything else is shown as no number at all.
function isCount(value) {
  return Number.isInteger(value) && value >= 0;
}

// Zero is written "0": JSON can carry -0, which toLocaleString keeps signed.
function plural(count, one, many) {
  return `${(count === 0 ? 0 : count).toLocaleString()} ${count === 1 ? one : many}`;
}

// archiveCountLine is what the header and the search box say about the
// archive's size: the card count from /api/meta and the set count from the
// search response's facets. Until a count arrives, or when its request
// failed, its part of the line simply leaves the number out.
export function archiveCountLine({ cards, sets } = {}) {
  return {
    cards: isCount(cards) ? plural(cards, "English card", "English cards") : "English cards",
    sets: isCount(sets) ? `${plural(sets, "set", "sets")} · 1999—today` : "1999—today",
    placeholder: isCount(cards) ? `Search ${plural(cards, "card", "cards")}…` : "Search cards…",
  };
}

// What the archive has reported so far: undefined until it answers, null
// when the request failed.
const archiveCounts = { cards: undefined, sets: undefined };

function paintArchiveCounts() {
  const line = archiveCountLine(archiveCounts);
  $("archive-cards").textContent = line.cards;
  $("archive-sets").textContent = line.sets;
  $("search-input").placeholder = line.placeholder;
}

// noteArchiveSets takes the set count from a search response. The facet lists
// every set in the catalog, with a zero count for those the search excludes,
// so its length is the same whatever was searched.
export function noteArchiveSets(sets) {
  if (!isCount(sets) || sets === archiveCounts.sets) return;
  archiveCounts.sets = sets;
  paintArchiveCounts();
}

// loadArchiveCounts asks the server how many cards it holds, once, at start.
export async function loadArchiveCounts() {
  paintArchiveCounts();
  try {
    const res = await fetch("/api/meta");
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    archiveCounts.cards = isCount(data?.docs) ? data.docs : null;
  } catch {
    archiveCounts.cards = null;
  }
  paintArchiveCounts();
}

export function setDegraded(isDegraded) {
  $("degraded-banner").hidden = !isDegraded;
  recordSearchOutcome(!isDegraded);
  // A failed search leaves the previous query's totals on screen, where they
  // read as a count of what is showing now. Blank them to a dash — the
  // elements and their live regions stay put, so the next success has
  // somewhere to land and the change is still announced.
  if (isDegraded) {
    $("total-count").textContent = BLANK;
    $("stat-results").textContent = BLANK;
    $("stat-roundtrip").textContent = BLANK;
  }
}

// pillStatus decides the service-status pill from the two things that can
// set it: the newest search's outcome (taken when it finished) and the newest
// health check (taken when it started). Whichever was taken later wins, so a
// health check already in flight when a search finished cannot overwrite it.
// A search that ended in an outage keeps the pill Degraded whatever the
// health check says, because the banner is up and the two must agree.
export function pillStatus({ search, health }) {
  if (search && !search.ok) return { label: "Degraded", degraded: true };
  if (health && (!search || health.at > search.at)) {
    return health.ok ? { label: "Online", degraded: false } : { label: "Degraded", degraded: true };
  }
  if (search) return { label: "Online", degraded: false };
  return { label: "Connecting", degraded: false };
}

// The two outcomes the pill is built from, and the clock that orders them.
let pillClock = 0;
let lastSearch = null;
let lastHealth = null;

function paintPill() {
  const { label, degraded } = pillStatus({ search: lastSearch, health: lastHealth });
  setServiceStatus(label, degraded);
}

// startHealthCheck marks the moment a health check is sent; the returned
// function records its result against that moment.
export function startHealthCheck() {
  const at = ++pillClock;
  return (ok) => {
    if (!lastHealth || at > lastHealth.at) lastHealth = { at, ok };
    paintPill();
  };
}

// recordSearchOutcome records a finished search: ok is false only for an
// outage, since any answer from the server means the service is up.
export function recordSearchOutcome(ok) {
  lastSearch = { at: ++pillClock, ok };
  paintPill();
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
export function quantile(sorted, p) {
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
  // A search landed, so the lab is stale if the query moved. It is the rail's
  // own panel, and this is the moment the rail learns a search completed —
  // which is why the refresh hangs here rather than off every call site that
  // can start one.
  refreshRankingLab();
}

// The Ranking lab: the same query ranked under two named weight profiles, and
// what moved between the two windows.
//
// It is the one panel in the rail that asks for something of its own, and it
// asks only while it is open — a comparison is two searches, and nobody is owed
// them per keystroke behind a closed panel. That policy is the panel's, which
// is why the request lives here rather than in api.js: api.js owns the search
// lifecycle, and this is not part of it.
const LAB_IDLE = "Open this panel with a search running to rank it under two weight profiles.";
const LAB_UNAVAILABLE = "The ranking comparison is unavailable right now.";

// The branches a profile moves, in the order ADR 10 reports them. The fourth,
// text, is in no profile: it carries Elasticsearch's implicit 1 everywhere, and
// is the unit the other three are ratios of.
const LAB_WEIGHTED_BRANCHES = ["exact", "prefix", "fuzzy-name"];

// How many movers the panel names. The rail is a column, not a table: five is
// what fits without turning the panel into a second results grid, and the
// server sends them largest-movement first, so five is the top five.
const LAB_MOVERS_SHOWN = 5;

// The query the panel is currently showing, or null when it is showing nothing.
// A comparison only depends on the query text — filters live in post_filter and
// cannot reorder anything — so a search that did not change it needs no refetch.
let labQuery = null;

// Only the newest comparison may paint. Typing produces several, and the one
// that answers last is not necessarily the one that was asked last.
let labRequest = 0;

export async function refreshRankingLab() {
  if (!$("ranking-lab").open) return;
  const q = queryText();
  if (!q) {
    labQuery = "";
    clearRankingLab(LAB_IDLE);
    return;
  }
  if (q === labQuery) return;

  const request = ++labRequest;
  labQuery = q;
  clearRankingLab("Ranking under both profiles…");
  try {
    const res = await fetch(`/api/compare?${new URLSearchParams({ q })}`);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    if (request !== labRequest) return;
    renderRankingLab(data);
  } catch {
    if (request !== labRequest) return;
    // Nothing is on screen, so the next search is allowed to try again rather
    // than being deduplicated against a comparison that never arrived.
    labQuery = null;
    clearRankingLab(LAB_UNAVAILABLE);
  }
}

function clearRankingLab(message) {
  $("lab-status").textContent = message;
  $("lab-columns").replaceChildren();
  $("lab-movers").replaceChildren();
}

function renderRankingLab(data) {
  const deltas = new Map((data.deltas ?? []).map((delta) => [delta.id, delta]));
  const counted = deltas.size;
  $("lab-status").textContent =
    `Spearman ρ ${formatRho(data.spearman_rho_union)} over ${counted} ${counted === 1 ? "card" : "cards"}`
    + ` · top ${data.window} of ${Number(data.a.total ?? 0).toLocaleString()}`;
  $("lab-columns").replaceChildren(labColumn(data.a, deltas), labColumn(data.b, deltas));
  $("lab-movers").replaceChildren(...labMovers(data.deltas ?? []));
}

// ρ is a correlation, not a measurement of anything countable, and the panel is
// two short lists wide: two decimals is as much of it as means anything here.
// Null is the server saying there was nothing to correlate, which is not zero.
function formatRho(rho) {
  return typeof rho === "number" ? rho.toFixed(2) : BLANK;
}

// setCodeAndNumber reads a card id as its set code and its number in that
// set. Ids are "<set>-<number>", and set codes can hold a dash of their own,
// so the split is on the last one; an id with no dash is all set code.
export function setCodeAndNumber(id) {
  const value = typeof id === "string" ? id : "";
  const at = value.lastIndexOf("-");
  if (at === -1) return { set: value, number: "" };
  return { set: value.slice(0, at), number: value.slice(at + 1) };
}

// labCardCode is the line under a lab row's name: the set code and the number,
// each left out when the id has none, so the line never starts with a space or
// ends on a lone "#".
export function labCardCode(id) {
  const { set, number } = setCodeAndNumber(id);
  return [set.trim(), number.trim() ? `#${number.trim()}` : ""].filter(Boolean).join(" ");
}

// labWeights labels each weight a profile carries with the badge word of the
// branch it boosts, so "8" reads as "exact ×8".
export function labWeights(boosts) {
  return LAB_WEIGHTED_BRANCHES.map((name) => {
    const value = boosts?.[name];
    return `${BRANCHES[name].label} ${value === undefined || value === null ? BLANK : `×${value}`}`;
  });
}

function labColumn(side, deltas) {
  const column = element("div", "lab-col");
  const weights = element("p", "lab-col-weights", labWeights(side.boosts).join(" · "));
  weights.title = "How much each kind of name match weighs; a text match counts ×1";
  column.append(element("p", "lab-col-name", side.profile), weights);
  const list = element("ul", "lab-list");
  for (const row of side.results ?? []) {
    const item = element("li", "lab-row");
    item.title = `${row.id} · score ${row.score}`;
    // The name keeps the cell's whole width; the set code and number sit on a
    // smaller line under it, so two cards with one name can be told apart.
    const card = element("span", "lab-card");
    card.append(element("span", "lab-name", row.name || row.id));
    const code = labCardCode(row.id);
    if (code) card.append(element("span", "lab-code", code));
    item.append(
      element("span", "lab-rank", String(row.rank)),
      card,
      labDelta(deltas.get(row.id)),
    );
    list.append(item);
  }
  column.append(list);
  return column;
}

// The mark beside a row. Every state carries its own glyph or word, so the
// colour behind it is emphasis and never the only way to read the row.
function labDelta(delta) {
  switch (delta?.status) {
    case "up":
      return element("span", "lab-delta is-up", `▲${delta.delta}`);
    case "down":
      return element("span", "lab-delta is-down", `▼${-delta.delta}`);
    case "entered":
      return element("span", "lab-delta is-entered", "new");
    case "dropped":
      return element("span", "lab-delta is-dropped", "out");
    default:
      return element("span", "lab-delta", "–");
  }
}

// The movers, in the order the server ranked them: what crossed the window edge
// first, then the largest moves inside it.
function labMovers(deltas) {
  const moved = deltas.filter((delta) => delta.status !== "same").slice(0, LAB_MOVERS_SHOWN);
  if (moved.length === 0) {
    return [element("li", "lab-movers-none", "Both profiles rank the same cards in the same order.")];
  }
  return moved.map((delta) => {
    const item = element("li");
    item.append(
      element("b", "", delta.name || delta.id),
      element("span", "", `${delta.rank_a ?? BLANK} → ${delta.rank_b ?? BLANK}`),
    );
    return item;
  });
}
