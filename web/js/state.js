// What is being searched, and the two translations of it: the address bar and
// the /api/search querystring. Those used to be written out by hand in two
// near-identical bodies that had to be kept in sync by eye; buildParams is now
// the single place the mapping lives.
//
// This module imports nothing app-level, which is what keeps the module graph
// acyclic (main → api/suggest/modal → render → telemetry → util).

export const defaults = {
  q: "",
  supertype: "",
  types: [],
  set: "",
  rarity: "",
  series: "",
  sort: "",
  order: "",
  page: 1,
};

export let state = { ...defaults };

export function readStateFromURL() {
  const sp = new URLSearchParams(location.search);
  state = { ...defaults };
  state.q = sp.get("q") ?? "";
  state.supertype = sp.get("supertype") ?? "";
  state.types = (sp.get("types") ?? "").split(",").filter(Boolean);
  state.set = sp.get("set") ?? "";
  state.rarity = sp.get("rarity") ?? "";
  state.series = sp.get("series") ?? "";
  state.sort = sp.get("sort") ?? "";
  state.order = sp.get("order") ?? "";
}

export function queryText() {
  return state.q.trim();
}

// buildParams serializes the current state. The address bar wants it without
// page or debug; the API call wants both — hence the two flags rather than two
// functions that would drift apart again.
export function buildParams({ page = false, debug = false } = {}) {
  const sp = new URLSearchParams();
  if (queryText()) sp.set("q", queryText());
  if (state.supertype) sp.set("supertype", state.supertype);
  if (state.types.length) sp.set("types", state.types.join(","));
  if (state.set) sp.set("set", state.set);
  if (state.rarity) sp.set("rarity", state.rarity);
  if (state.series) sp.set("series", state.series);
  if (state.sort) sp.set("sort", state.sort);
  if (state.order) sp.set("order", state.order);
  if (page && state.page > 1) sp.set("page", String(state.page));
  if (debug) sp.set("debug", "1");
  return sp;
}

// writeStateToURL mirrors the state into the address bar. A user-initiated
// change asks for push, so Back and Forward walk the searches they made; a
// programmatic one replaces, so the history does not fill with steps nobody
// took. Push is downgraded to replace when the querystring did not actually
// change, or Back would step through a run of identical entries.
// It returns whether a new entry was pushed, so the caller can tell a typing
// burst's first entry from one it only rewrote.
export function writeStateToURL({ push = false } = {}) {
  const qs = buildParams().toString();
  const url = `${location.pathname}${qs ? `?${qs}` : ""}${location.hash}`;
  if (push && addressDiffersFromState()) {
    history.pushState(null, "", url);
    return true;
  }
  history.replaceState(null, "", url);
  return false;
}

// addressDiffersFromState says whether writing the state now would change the
// querystring in the address bar.
export function addressDiffersFromState() {
  return buildParams().toString() !== new URLSearchParams(location.search).toString();
}

// The fields the address carries. A 400 that names one of them is the
// visitor's input, and the page can drop it and search again.
const ADDRESS_FIELDS = ["q", "supertype", "types", "set", "rarity", "series", "sort", "order"];

// withoutField returns a copy of a state with one field back at its default.
// The input is left alone, and the copy never shares a list with it.
export function withoutField(from, field) {
  const next = { ...from, types: [...from.types] };
  if (Object.hasOwn(defaults, field)) {
    next[field] = Array.isArray(defaults[field]) ? [] : defaults[field];
  }
  return next;
}

// A value quoted back to the visitor is cut short: a pasted address can hold
// a parameter thousands of characters long. The limit counts characters, not
// UTF-16 units, so a cut never splits an emoji in half.
const NOTICE_VALUE_LIMIT = 40;

function quoteParam({ field, value }) {
  const chars = Array.from(value);
  const shown = chars.length > NOTICE_VALUE_LIMIT ? `${chars.slice(0, NOTICE_VALUE_LIMIT).join("")}…` : value;
  return `"${field}=${shown}"`;
}

// ignoredNotice is the sentence shown after the page dropped parameters the
// server refused and searched without them.
export function ignoredNotice(dropped) {
  if (!dropped.length) return "";
  const quoted = dropped.map(quoteParam);
  if (quoted.length === 1) return `Ignored ${quoted[0]}: not a valid value.`;
  const list = `${quoted.slice(0, -1).join(", ")} and ${quoted[quoted.length - 1]}`;
  return `Ignored ${list}: not valid values.`;
}

const TOO_LONG = "That search is too long to run. Try a shorter one.";

function paramValue(from, field) {
  return field === "types" ? from.types.join(",") : String(from[field] ?? "");
}

function withIgnored(dropped, sentence) {
  const ignored = ignoredNotice(dropped);
  return ignored ? `${ignored} ${sentence}` : sentence;
}

// failureAction decides what a failed search means. A network failure or a
// 5xx is an outage: the banner and its retry. Anything else is an answer
// about the request itself, so it never raises the banner. A 400 that names a
// field the page holds drops that field and searches again — each field at
// most once per search, so the retries end. The query text is never dropped:
// a query the server finds too long stays in the box for the visitor to
// shorten.
export function failureAction({ status, body, state: from, dropped = [] }) {
  if (!status || status >= 500) return { action: "outage" };
  const error = body && typeof body === "object" && body.error && typeof body.error === "object" ? body.error : null;
  const field = typeof error?.field === "string" ? error.field : "";
  if (status === 400 && field === "q") {
    return { action: "notice", notice: withIgnored(dropped, TOO_LONG) };
  }
  if (status === 400 && ADDRESS_FIELDS.includes(field)
    && !dropped.some((entry) => entry.field === field)) {
    const value = paramValue(from, field);
    if (value) return { action: "retry", field, value };
  }
  const message = typeof error?.message === "string" ? error.message.trim().replace(/\.+$/, "") : "";
  const sentence = `PokéSearch could not run that search${message ? `: ${message}` : ""}.`;
  return { action: "notice", notice: withIgnored(dropped, sentence) };
}

// historyAction decides what a search writes to the address bar. Typing
// pushes once, when a burst starts on an entry the visitor already finished
// with, and only rewrites that entry afterwards; a draft entry is the one a
// burst created and has not finished. Enter, blur and a second of idle
// finish the query: they keep a draft in place, push a query the address
// does not hold yet, and do nothing when it already does. Suggestions and
// filters push as they always have, Back and Forward write nothing, and
// anything else (a retry, a refresh) only rewrites.
export function historyAction(trigger, { draft, changed }) {
  switch (trigger) {
    case "typing":
      return changed && !draft ? "push" : "replace";
    case "enter":
    case "blur":
    case "idle":
      if (!changed) return "none";
      return draft ? "replace" : "push";
    case "suggestion":
    case "filter":
      return "push";
    case "popstate":
      return "none";
    default:
      return "replace";
  }
}

// The server applies the same two defaults; the UI has to know them to render
// the sort control truthfully before the response arrives.
export function effectiveSort() {
  const sort = state.sort || (queryText() ? "relevance" : "newest");
  return sort === "relevance" && !queryText() ? "newest" : sort;
}

export function effectiveOrder() {
  if (state.order) return state.order;
  if (effectiveSort() === "hp") return "desc";
  if (effectiveSort() === "name") return "asc";
  return "";
}

// freshHistoryMemo is what the page remembers about history when no typing
// burst is open: no draft entry, and so no address beneath one.
export function freshHistoryMemo() {
  return { draft: false, below: null };
}

// historyStep is the whole history decision for one search. Given what
// started it, what the page remembers (memo), the querystring the address
// holds and the one the state wants, it returns what to do — "push",
// "replace", "back" or "none" — and the memo to keep.
//
// A typing burst pushes one draft entry and remembers the address beneath it.
// Typing back to that address steps back to it instead of leaving two equal
// entries side by side. Everything that finishes a query (Enter, blur, idle,
// a suggestion, a filter) closes the draft, whatever it writes, so the next
// burst cannot rewrite the finished entry. Back and Forward write nothing
// and forget the draft.
export function historyStep({ trigger, memo, address, wanted }) {
  if (trigger === "popstate") return { action: "none", memo: freshHistoryMemo() };
  if (memo.draft && wanted === memo.below && wanted !== address) {
    return { action: "back", memo: freshHistoryMemo() };
  }
  const changed = wanted !== address;
  switch (trigger) {
    case "typing": {
      const action = historyAction("typing", { draft: memo.draft, changed });
      if (action === "push") return { action, memo: { draft: true, below: address } };
      return { action, memo: { ...memo } };
    }
    case "enter":
    case "blur":
    case "idle":
      return { action: historyAction(trigger, { draft: memo.draft, changed }), memo: freshHistoryMemo() };
    case "suggestion":
    case "filter":
      return { action: changed ? "push" : "replace", memo: freshHistoryMemo() };
    default:
      return { action: "replace", memo: { ...memo } };
  }
}
