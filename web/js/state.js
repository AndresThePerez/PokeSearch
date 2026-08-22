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

export function writeStateToURL() {
  const qs = buildParams().toString();
  history.replaceState(null, "", `${location.pathname}${qs ? `?${qs}` : ""}${location.hash}`);
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
