// Every call to the PokéSearch API, and the request lifecycle around it:
// debouncing, in-flight cancellation and the degraded-state signal.
//
// Imports state, render and telemetry — never suggest.js or modal.js, which
// import this module instead. That direction is what keeps the graph acyclic.

import {
  state,
  buildParams,
  writeStateToURL,
  failureAction,
  withoutField,
  ignoredNotice,
  historyStep,
  freshHistoryMemo,
} from "./state.js";
import { renderResults, setLoading, setStale, syncControls, syncFilterControls } from "./render.js";
import { renderInspector, setDegraded, startHealthCheck, debugEnabled, recordTiming } from "./telemetry.js";
import { $ } from "./util.js";

let searchController = null;
// How many searches are on their way, and which one started last: the query
// panel's own request (see refreshQuery) only paints if no search began after it.
let searchesRunning = 0;
let searchSerial = 0;
let debounceTimer = null;
let idleTimer = null;

// What the page remembers about history between searches: whether the
// current entry is a draft a typing burst pushed, and the address beneath it.
// historyStep decides; this module only carries the decision out.
let historyMemo = freshHistoryMemo();

// The querystring a step back is heading for, until its popstate arrives.
let stepBackTo = null;

// The query counts as finished after this long with no keystroke.
const IDLE_COMMIT_MS = 1000;

// The number of cards a search asks for. It is the server's own default, sent
// anyway so that the request and the loading skeleton read one number.
export const SEARCH_PAGE_SIZE = 24;

// searchRequestParams is the querystring of a search request: the state, the
// page it is on, the page size and, when the inspector wants it, debug.
export function searchRequestParams({ debug = false } = {}) {
  const sp = buildParams({ page: true, debug });
  sp.set("page_size", String(SEARCH_PAGE_SIZE));
  return sp;
}

function addressQuery() {
  return new URLSearchParams(location.search).toString();
}

function planHistory(trigger) {
  return historyStep({ trigger, memo: historyMemo, address: addressQuery(), wanted: buildParams().toString() });
}

// writeHistory carries out historyStep's decision for what started the search.
function writeHistory(trigger) {
  const { action, memo } = planHistory(trigger);
  historyMemo = memo;
  if (action === "push" || action === "replace") {
    writeStateToURL({ push: action === "push" });
  } else if (action === "back") {
    // The entry beneath the draft already holds this query. Its popstate is
    // the page's own doing and is ignored (see isOwnStepBack).
    stepBackTo = buildParams().toString();
    history.back();
  }
}

// stepBackHolds says whether the step back under way already lands on the
// query the page wants. Until its popstate arrives the address still shows
// the draft, and a commit then (focus leaving the box right after Enter)
// would take the query for a new one and push it on top of the draft.
export function stepBackHolds({ stepBackTo: heading, wanted }) {
  return heading !== null && heading === wanted;
}

// searchBoxStatus is what Enter needs to know about the typed query: whether
// a keystroke search is waiting on the debounce, and whether the query
// differs from the one the address holds, which is the one the grid shows.
export function searchBoxStatus() {
  const wanted = buildParams().toString();
  return {
    pending: debounceTimer !== null,
    changed: wanted !== addressQuery() && !stepBackHolds({ stepBackTo, wanted }),
  };
}

// isOwnStepBack tells the popstate listener that this popstate is the step
// back writeHistory asked for, which needs no search and no write: the search
// for that query is already running. It answers once.
export function isOwnStepBack() {
  const expected = stepBackTo;
  stepBackTo = null;
  return expected !== null && expected === addressQuery();
}

// noteHistoryNavigation is called on every Back or Forward the visitor makes.
// A keystroke search or commit still waiting would write against the entry
// they just landed on, so both are dropped, and the draft is forgotten. It
// returns whether a keystroke search was dropped: the grid then still shows
// a text the visitor had already typed past.
export function noteHistoryNavigation() {
  const cancelled = debounceTimer !== null;
  cancelScheduledSearch();
  historyMemo = historyStep({ trigger: "popstate", memo: historyMemo, address: "", wanted: "" }).memo;
  return cancelled;
}

// popstateRunsSearch decides whether a Back or Forward searches. The page's
// own step back never does: that search is already running. Otherwise it
// searches when the entry it landed on holds a different search, or when it
// dropped a keystroke search, because the grid then shows results for text
// the box no longer holds even if the address and the box agree.
export function popstateRunsSearch({ ownStepBack, stateChanged, cancelledSearch }) {
  if (ownStepBack) return false;
  return stateChanged || cancelledSearch;
}

// noticeView decides how the notice under the search controls looks for a
// given text. The live region itself is never hidden — a status region that
// appears already filled is often not announced — so an empty notice only
// drops its box and takes its Dismiss button out of the tab order.
export function noticeView(text) {
  const trimmed = typeof text === "string" ? text.trim() : "";
  return { text: trimmed, shown: trimmed !== "", dismissHidden: trimmed === "" };
}

// The notice under the search controls: what the page ignored, or why a
// search could not run. Plain text, and it never stacks with the banner.
export function showNotice(message) {
  const { text, shown, dismissHidden } = noticeView(message);
  $("search-notice").classList.toggle("is-shown", shown);
  $("search-notice-dismiss").hidden = dismissHidden;
  $("search-notice-text").textContent = text;
}

export function hideNotice() {
  showNotice("");
}

async function readErrorBody(res) {
  try {
    return await res.json();
  } catch {
    return null;
  }
}

// push marks a search the user asked for, which earns a history entry.
// fromHistory marks one Back or Forward produced, which must not write history
// back — that is how you get a Back button that cannot leave the current entry.
// trigger names a search that came from the search box ("typing", "enter",
// "blur", "idle"); dropped lists the parameters this search already dropped
// after the server refused them.
// It resolves to how the search ended: "results", "none" (it found nothing),
// "refused" (the archive answered that it would not run it), "failed" (no
// answer) or "aborted" (a newer search replaced it).
export async function runSearch({ append = false, push = false, fromHistory = false, trigger = "", dropped = [] } = {}) {
  if (!append) state.page = 1;
  if (!fromHistory) writeHistory(trigger || (push ? "filter" : "refresh"));
  // Only the newest search may paint. An aborted one is not a failure and must
  // not raise the degraded banner.
  searchController?.abort();
  const controller = new AbortController();
  const startedAt = performance.now();
  searchController = controller;
  searchSerial += 1;
  searchesRunning += 1;
  setLoading(true, { append, pageSize: SEARCH_PAGE_SIZE });
  try {
    const res = await fetch(`/api/search?${searchRequestParams({ debug: debugEnabled() })}`, { signal: controller.signal });
    if (!res.ok) {
      const body = await readErrorBody(res);
      if (controller.signal.aborted) return "aborted";
      const decision = failureAction({ status: res.status, body, state, dropped });
      if (decision.action === "retry") {
        // The server named a parameter it will not take. Drop it, rewrite the
        // address without it, and search again; the field cannot come back
        // in this search, so the retries end.
        Object.assign(state, withoutField(state, decision.field));
        syncControls();
        syncFilterControls();
        return runSearch({ append, dropped: [...dropped, { field: decision.field, value: decision.value }] });
      }
      if (decision.action === "notice") {
        // The archive answered, so this is not an outage. The cards under the
        // notice are still the previous search's.
        setDegraded(false);
        setStale(true);
        showNotice(decision.notice);
        return "refused";
      }
      throw new Error(`HTTP ${res.status}`);
    }
    const data = await res.json();
    const roundTripMs = performance.now() - startedAt;
    renderResults(data, { append, roundTripMs });
    renderInspector(data.dsl, data);
    // The request id comes off the response header rather than the body: every
    // response carries it, including the ones with no body worth reading.
    recordTiming(data.took_ms, roundTripMs, res.headers.get("X-Request-Id"));
    setDegraded(false);
    setStale(false);
    if (dropped.length) showNotice(ignoredNotice(dropped));
    else hideNotice();
    return data.total === 0 ? "none" : "results";
  } catch (err) {
    if (err.name === "AbortError") return "aborted";
    hideNotice();
    setDegraded(true);
    // The grid still holds the last successful search's cards. Mark them as
    // what they are rather than let the banner and the results contradict
    // each other.
    setStale(true);
    return "failed";
  } finally {
    searchesRunning -= 1;
    if (searchController === controller) {
      setLoading(false, { append });
    }
  }
}

// queryPanelAction decides what opening the query panel asks for. The first
// answer on a phone carries no query (the panel was closed, so none was
// requested), and fetching it must not search the cards again: that would
// reset the list to its first page. With no search running, the query alone
// is asked for; with one running, that search was started before the panel
// opened and the panel still needs a search that asks for the query, as it
// always has.
export function queryPanelAction({ open, hadQuery, searching } = {}) {
  if (open !== true || hadQuery === true) return "none";
  return searching === true ? "search" : "query-only";
}

// queryPanelRequest is the call main.js makes when the panel opens or closes.
export function queryPanelRequest(open, hadQuery) {
  return queryPanelAction({ open, hadQuery, searching: searchesRunning > 0 });
}

const QUERY_UNAVAILABLE = "The query could not be loaded just now. Search again to see it.";

// refreshQuery asks for the search on screen once more with its query
// requested, and fills the rail's two panes from the answer. The cards, the
// counts, the address, the history, the status pill and the timings stay as
// they are. It paints only if no search began after it, since that search
// brings its own query.
export async function refreshQuery() {
  const serial = searchSerial;
  try {
    const res = await fetch(`/api/search?${searchRequestParams({ debug: true })}`);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    if (serial === searchSerial) renderInspector(data.dsl, data);
  } catch {
    if (serial === searchSerial) $("dsl-json").textContent = QUERY_UNAVAILABLE;
  }
}

// The debounce collapses a typing burst into one search. Its history entry is
// pushed once, when the burst starts, and rewritten after that; a second with
// no keystroke finishes the query, so the next burst gets an entry of its own.
export function scheduleSearch() {
  clearTimeout(debounceTimer);
  clearTimeout(idleTimer);
  debounceTimer = setTimeout(() => {
    debounceTimer = null;
    runSearch({ trigger: "typing" });
  }, 200);
  idleTimer = setTimeout(() => commitQuery("idle"), IDLE_COMMIT_MS);
}

// commitQuery finishes the query in the search box: Enter, leaving the box,
// or the idle timer. A keystroke search still waiting on the debounce runs
// now instead; a query the address already holds writes nothing. It returns
// the search it started, which resolves to how that search ended, or null
// when it started none.
export function commitQuery(trigger) {
  clearTimeout(idleTimer);
  if (stepBackHolds({ stepBackTo, wanted: buildParams().toString() })) return null;
  const { action, memo } = planHistory(trigger);
  if (action === "none") {
    historyMemo = memo;
    return null;
  }
  clearTimeout(debounceTimer);
  debounceTimer = null;
  return runSearch({ trigger });
}

// cancelScheduledSearch drops a pending debounce. Picking a suggestion runs a
// search immediately, and the queued keystroke search must not land after it.
export function cancelScheduledSearch() {
  clearTimeout(debounceTimer);
  clearTimeout(idleTimer);
  debounceTimer = null;
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

// fetchLinkedCard backs the #card= deep link when the card is not already in
// the grid — the id fast path returns exactly one document. It never throws:
// it hands back the HTTP status (0 when there was no answer), the card or
// null, and whether the request failed before an answer could be read, which
// is what the dialog needs to tell a card that is not there from an archive
// that did not answer.
export async function fetchLinkedCard(id) {
  const sp = new URLSearchParams({ id });
  let res;
  try {
    res = await fetch(`/api/search?${sp}`);
  } catch {
    return { status: 0, card: null, networkError: true };
  }
  if (!res.ok) return { status: res.status, card: null, networkError: false };
  try {
    const data = await res.json();
    return { status: res.status, card: data.results?.[0] ?? null, networkError: false };
  } catch {
    return { status: res.status, card: null, networkError: true };
  }
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

// fetchStats loads the corpus analytics behind the Stats view. The server
// computes them once and serves them from memory afterwards, so this is a
// cheap call — but it still goes through here, because every request this app
// makes lives in this module.
export async function fetchStats(debug = false) {
  const sp = new URLSearchParams();
  if (debug) sp.set("debug", "1");
  const query = sp.toString();
  const res = await fetch(`/api/stats${query ? `?${query}` : ""}`);
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

// fetchTopCard returns the first card under a sort. The Stats view's
// superlatives line needs the name behind the max-HP number, and a
// page_size=1 search is the cheapest way to ask for it.
export async function fetchTopCard(sort) {
  const sp = new URLSearchParams({ sort, page_size: "1" });
  const res = await fetch(`/api/search?${sp}`);
  if (!res.ok) return null;
  const data = await res.json();
  return data.results[0] ?? null;
}

// The health check feeds the status pill, but only a check started after the
// newest search finished may change it: the pill is the newest outcome's.
export async function refreshHealth() {
  const settle = startHealthCheck();
  try {
    const res = await fetch("/healthz");
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    $("stat-indexed").textContent = Number(data.docs).toLocaleString();
    settle(true);
  } catch {
    $("stat-indexed").textContent = "Unavailable";
    settle(false);
  }
}
