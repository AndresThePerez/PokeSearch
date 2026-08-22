// Every call to the Pokesearch API, and the request lifecycle around it:
// debouncing, in-flight cancellation and the degraded-state signal.
//
// Imports state, render and telemetry — never suggest.js or modal.js, which
// import this module instead. That direction is what keeps the graph acyclic.

import { state, buildParams, writeStateToURL } from "./state.js";
import { renderResults, setLoading } from "./render.js";
import { renderInspector, setDegraded, setServiceStatus, debugEnabled } from "./telemetry.js";
import { $ } from "./util.js";

let searchController = null;
let debounceTimer = null;

// push marks a search the user asked for, which earns a history entry.
// fromHistory marks one Back or Forward produced, which must not write history
// back — that is how you get a Back button that cannot leave the current entry.
export async function runSearch({ append = false, push = false, fromHistory = false } = {}) {
  if (!append) state.page = 1;
  if (!fromHistory) writeStateToURL({ push });
  // Only the newest search may paint. An aborted one is not a failure and must
  // not raise the degraded banner.
  searchController?.abort();
  const controller = new AbortController();
  const startedAt = performance.now();
  searchController = controller;
  setLoading(true, { append });
  try {
    const res = await fetch(`/api/search?${buildParams({ page: true, debug: debugEnabled() })}`, { signal: controller.signal });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    renderResults(data, { append, roundTripMs: performance.now() - startedAt });
    renderInspector(data.dsl, data);
    setDegraded(false);
  } catch (err) {
    if (err.name === "AbortError") return;
    setDegraded(true);
  } finally {
    if (searchController === controller) {
      setLoading(false, { append });
    }
  }
}

// The debounce already collapses a typing burst into one search, so one burst
// leaves exactly one history entry — Back steps back a query, not a keystroke.
export function scheduleSearch() {
  clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => runSearch({ push: true }), 200);
}

// cancelScheduledSearch drops a pending debounce. Picking a suggestion runs a
// search immediately, and the queued keystroke search must not land after it.
export function cancelScheduledSearch() {
  clearTimeout(debounceTimer);
}

// fetchSuggestions is the network half of autocomplete; suggest.js owns the
// timer, the AbortController and the list rendering.
export async function fetchSuggestions(q, signal) {
  const sp = new URLSearchParams({ q });
  const res = await fetch(`/api/suggest?${sp}`, { signal });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const data = await res.json();
  return data.suggestions ?? [];
}

// fetchCardByID backs the #card= deep link when the card is not already in the
// grid — the id fast path returns exactly one document.
export async function fetchCardByID(id) {
  const sp = new URLSearchParams({ id });
  const res = await fetch(`/api/search?${sp}`);
  if (!res.ok) return null;
  const data = await res.json();
  return data.results[0] ?? null;
}

// fetchExplain asks the server to score one card against the current query,
// branch by branch. It is the on-demand half of the relevance X-Ray: one
// single-document call per click, never per keystroke.
export async function fetchExplain(id, q) {
  const sp = new URLSearchParams({ id, q });
  const res = await fetch(`/api/explain?${sp}`);
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export async function refreshHealth() {
  try {
    const res = await fetch("/healthz");
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    $("stat-indexed").textContent = Number(data.docs).toLocaleString();
    setServiceStatus("Online");
  } catch {
    $("stat-indexed").textContent = "Unavailable";
    setServiceStatus("Degraded", true);
  }
}
