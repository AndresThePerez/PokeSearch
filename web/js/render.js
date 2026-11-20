// Everything that paints a response: the card grid, the facet controls, the
// active-filter chips, the loading skeleton and the sort control.
//
// This module must not import api.js, or the graph becomes cyclic. The one
// thing it needs from there — "the user removed a filter, search again" — is
// injected by main.js through setFilterChangeHandler.

import { $, element } from "./util.js";
import { state, queryText, effectiveSort, effectiveOrder } from "./state.js";
import { renderStats, noteArchiveSets, BRANCHES } from "./telemetry.js";

// The rendered cards, keyed by id, so the modal and the #card= deep link can
// open one without a second request.
export const cardsByID = new Map();

// Set id → human label, filled from the response's set catalog so the active
// filter chip can say "Base" rather than "base1".
export const setLabels = new Map();

let onFilterChange = () => {};

// setFilterChangeHandler injects the "search again" call the active-filter
// chips need, keeping render.js free of any dependency on api.js.
export function setFilterChangeHandler(fn) {
  onFilterChange = fn;
}


// renderDidYouMean offers the server's spelling correction on the empty state.
// It only ever appears on a zero-result search, so it cannot compete with the
// autocomplete list the user was being offered while typing.
//
// Unlike the filter chips, this button is static markup that is never
// replaced, so main.js binds its click once at startup and this only has to
// paint it — no injected handler needed.
function renderDidYouMean(suggestion) {
  const button = $("did-you-mean");
  button.hidden = !suggestion;
  if (!suggestion) return;
  button.replaceChildren(
    document.createTextNode("Did you mean "),
    element("strong", "", suggestion),
    document.createTextNode("?"),
  );
  button.setAttribute("aria-label", `Search for ${suggestion} instead`);
  button.dataset.suggestion = suggestion;
}

export function renderResults(data, { append = false, roundTripMs = 0 } = {}) {
  state.page = data.page;
  renderGrid(data.results, { append, page: data.page, matched: data.matched, highlights: data.highlights });
  renderFacets(data.facets);
  $("total-count").textContent = countLineText({ phase: "done", total: data.total });
  noteArchiveSets(data.facets?.sets?.length);
  $("load-more").hidden = data.pages === 0 || data.page >= data.pages;
  $("results-end").textContent = endOfResultsLine({
    shown: (data.page - 1) * data.page_size + data.results.length,
    total: data.total,
    window: data.pages * data.page_size,
  });
  $("empty-state").hidden = data.total !== 0;
  if (data.total === 0) $("empty-message").textContent = emptyStateMessage(state);
  renderDidYouMean(data.did_you_mean);
  $("results-grid").hidden = data.total === 0;
  renderGuide({ total: data.total, matched: data.matched });
  renderStats(data, roundTripMs);
  syncControls();
}

// endOfResultsLine is the line under the last page the archive will serve
// when the result runs past it. window is how many cards the pages reach
// (pages times page size), so a result that fits ends with no line at all.
export function endOfResultsLine({ shown, total, window }) {
  if (!(window > 0) || shown < window || total <= shown) return "";
  return `Showing the first ${Number(shown).toLocaleString()} of ${Number(total).toLocaleString()} cards. Narrow the search or add filters to see the rest.`;
}

// A query quoted back in the empty state is cut short: the box takes up to
// 256 characters, and the sentence has to stay one.
const EMPTY_QUERY_LIMIT = 40;

function hasFilters(from) {
  return Boolean(from.supertype || from.types?.length || from.set || from.rarity || from.series);
}

// emptyStateMessage is the sentence a search that found nothing shows, built
// from what the search asked for: the query it names, the filters that may be
// excluding results, or, for a browse with neither, a plain line.
export function emptyStateMessage(from) {
  const chars = Array.from(String(from.q ?? "").trim());
  const q = chars.length > EMPTY_QUERY_LIMIT ? `${chars.slice(0, EMPTY_QUERY_LIMIT).join("")}…` : chars.join("");
  if (hasFilters(from)) {
    return q
      ? `No cards match "${q}" with these filters, which may be excluding results.`
      : "No cards match these filters, which may be excluding results.";
  }
  return q ? `No cards match "${q}".` : "No cards to show.";
}

// The word each branch's badge shows, read off BRANCHES. Lookups go through
// Object.hasOwn, so a branch named like an object key ("constructor") is shown
// under its own name rather than as whatever the prototype holds.
const BRANCH_LABELS = Object.fromEntries(Object.entries(BRANCHES).map(([name, { label }]) => [name, label]));

// branchLabel is the word a branch's badge shows, or, for a branch the server
// added after this page was written, its raw name, so it shows up rather than
// vanishing.
export function branchLabel(name) {
  return Object.hasOwn(BRANCH_LABELS, name) ? BRANCH_LABELS[name] : name;
}

function branchMeaning(name) {
  return Object.hasOwn(BRANCHES, name) ? BRANCHES[name].meaning : `the ${name} part of the search`;
}

// badgeTitle is a badge's tooltip and accessible name: its word and what it
// means, in the words the legend uses.
export function badgeTitle(name) {
  return `${branchLabel(name)}: ${branchMeaning(name)}`;
}

// cardBranches reads one card's matched branches from a response. matched
// comes from the server: a shape it should not have means no badges, and only
// non-empty strings count as branch names.
export function cardBranches(matched, index) {
  const row = Array.isArray(matched) ? matched[index] : undefined;
  return Array.isArray(row) ? row.filter((name) => typeof name === "string" && name !== "") : [];
}

// badgeLegend is the legend for a page of results, given each card's matched
// branches. Results with no badge get none. Otherwise it names every branch
// the page knows, in badge order, whether or not this page shows it, so the
// line reads the same on every search; a branch the page has no words for is
// added after them, in the order it first appears.
export function badgeLegend(matched) {
  const seen = (Array.isArray(matched) ? matched : []).flatMap((_, index) => cardBranches(matched, index));
  if (seen.length === 0) return [];
  const names = [...Object.keys(BRANCHES)];
  for (const name of seen) if (!names.includes(name)) names.push(name);
  return names.map((name) => ({ name, label: branchLabel(name), meaning: branchMeaning(name) }));
}

// badgeStrip renders which relevance clauses ES says matched this card. It is
// aria-hidden because the same information goes into the button's accessible
// name as a sentence — four loose words read out per card is not an
// improvement over one. Each badge still carries its meaning as a tooltip.
function badgeStrip(names) {
  const strip = element("span", "match-badges");
  strip.setAttribute("aria-hidden", "true");
  for (const name of names) {
    const badge = element("i", `match-badge match-${name}`, branchLabel(name));
    badge.title = badgeTitle(name);
    strip.append(badge);
  }
  return strip;
}

// The searches the landing offers, and the empty state after a search that
// found nothing: one per part of the ranking, each with what it shows.
export const EXAMPLE_QUERIES = Object.freeze([
  Object.freeze({ q: "charizard", shows: "exact name" }),
  Object.freeze({ q: "pika", shows: "start of a name" }),
  Object.freeze({ q: "pikuchu", shows: "a typo" }),
  Object.freeze({ q: "flip a coin", shows: "card text" }),
  Object.freeze({ q: "Mitsuhiro Arita", shows: "artist" }),
]);

// examplePlacement decides where the example searches go: under the intro on
// a page with no query and no filters (from the first paint, before its
// results land), under the message of a search that found nothing, and
// nowhere on a page whose search has, or is waiting on, results.
export function examplePlacement({ state: from, total }) {
  if (total === 0) return "empty";
  if (!String(from.q ?? "").trim() && !hasFilters(from)) return "landing";
  return "none";
}

function exampleButton({ q, shows }) {
  const button = element("button", "example-query");
  button.type = "button";
  button.dataset.q = q;
  button.append(element("span", "example-q", q), document.createTextNode(" "), element("span", "example-shows", shows));
  return button;
}

// renderExamples fills both places the examples can appear, once, at start.
export function renderExamples() {
  for (const group of [$("landing-examples"), $("empty-examples")]) {
    group.replaceChildren(...EXAMPLE_QUERIES.map(exampleButton));
  }
}

function renderBadgeLegend(entries) {
  const legend = $("badge-legend");
  legend.hidden = entries.length === 0;
  const list = element("ul");
  for (const { name, label, meaning } of entries) {
    const item = element("li");
    item.append(element("i", `match-badge match-${name}`, label), document.createTextNode(` ${meaning}`));
    list.append(item);
  }
  legend.replaceChildren(element("span", "badge-legend-title", "Matched:"), list);
}

// renderGuide shows or hides what explains the page around the grid: the
// intro, the examples and the badge legend. total is undefined while the
// first search is on its way; a page with a query then keeps the legend's
// line, so the grid does not move down when the badges land.
export function renderGuide({ total, matched } = {}) {
  const placement = examplePlacement({ state, total });
  $("landing").hidden = placement !== "landing";
  $("empty-guide").hidden = placement !== "empty";
  const pending = total === undefined && queryText() ? [Object.keys(BRANCHES)] : [];
  renderBadgeLegend(badgeLegend(total === undefined ? pending : total > 0 ? matched : []));
}

// markNodes is the ONLY place a <mark> marker is ever interpreted, and it does
// it by building DOM nodes — innerHTML is never involved, here or anywhere
// else in the frontend. The marked text goes in as textContent, so a fragment
// carrying any other markup renders as literal characters rather than as
// elements. The server chose <mark> as the tag precisely so this parser has
// exactly one token to know about.
export function markNodes(fragment) {
  const nodes = [];
  let rest = String(fragment);
  while (rest.length) {
    const open = rest.indexOf("<mark>");
    if (open === -1) {
      nodes.push(document.createTextNode(rest));
      break;
    }
    if (open > 0) nodes.push(document.createTextNode(rest.slice(0, open)));
    const close = rest.indexOf("</mark>", open);
    const inner = close === -1 ? rest.slice(open + 6) : rest.slice(open + 6, close);
    const mark = document.createElement("mark");
    mark.textContent = inner;
    nodes.push(mark);
    rest = close === -1 ? "" : rest.slice(close + 7);
  }
  return nodes;
}

function stripMarks(fragment) {
  return fragment.replaceAll("<mark>", "").replaceAll("</mark>", "");
}

// markWithin re-applies a highlight to the full original string. ES returns
// body text as a short fragment, so rendering the fragment directly in the
// modal would silently truncate the card's own text; instead the fragment is
// located inside the original and only the marks are spliced in. A whole-value
// highlight (the name fields) is the same operation with an offset of zero.
//
// Returns null when no fragment belongs to this string — the caller falls back
// to plain text, which is what a deep-linked card with no search gets.
export function markWithin(original, fragments) {
  if (!original || !fragments?.length) return null;
  for (const fragment of fragments) {
    const plain = stripMarks(fragment);
    if (!plain) continue;
    const at = locateOccurrence(original, plain);
    if (at === -1) continue;
    const nodes = [];
    if (at > 0) nodes.push(document.createTextNode(original.slice(0, at)));
    nodes.push(...markNodes(fragment));
    const end = at + plain.length;
    if (end < original.length) nodes.push(document.createTextNode(original.slice(end)));
    return nodes;
  }
  return null;
}

// isWordChar treats letters, digits and underscore as "inside a word" — the
// class a real excerpt boundary never lands in the middle of.
function isWordChar(ch) {
  return ch !== undefined && /[\p{L}\p{N}_]/u.test(ch);
}

// locateOccurrence finds where `plain` sits inside `original`. A short
// fragment can legitimately repeat verbatim — the same word or clause
// appearing twice in the same text — and the first hit is not always the one
// the fragment was cut from. A whole-value fragment (plain === original) can
// only ever match at offset zero, so there is nothing to pick between; for a
// genuine excerpt, each repeat is checked against the fragment's own
// neighbours: an ES-cut excerpt never starts or ends mid-word, so the
// occurrence bordered by a non-word character (or the edge of the string) on
// both sides is the one it came from. When none or more than one repeat
// qualifies, nothing discriminates and the first occurrence stands.
function locateOccurrence(original, plain) {
  const first = original.indexOf(plain);
  if (first === -1 || plain === original) return first;
  let clean = -1;
  let cleanCount = 0;
  for (let at = first; at !== -1; at = original.indexOf(plain, at + 1)) {
    const end = at + plain.length;
    const before = at > 0 ? original[at - 1] : undefined;
    const after = end < original.length ? original[end] : undefined;
    if (!isWordChar(before) && !isWordChar(after)) {
      clean = at;
      cleanCount++;
    }
  }
  return cleanCount === 1 ? clean : first;
}

// Which highlighted field becomes the card's one-line snippet, in priority
// order. The card's own name is deliberately absent: it is already printed on
// the card and repeating it under the art explains nothing. An attack or
// ability *name* is included though — for q=whirlwind that is the only field
// that matches, and it is exactly the "why is this Pidgey here?" answer.
const SNIPPET_FIELDS = [
  ["attacks.name", "Attack"],
  ["abilities.name", "Ability"],
  ["attacks.text", "Attack"],
  ["abilities.text", "Ability"],
  ["flavor_text", "Flavor"],
];

function cardSnippet(highlight) {
  for (const [field, label] of SNIPPET_FIELDS) {
    const fragment = highlight?.[field]?.[0];
    if (!fragment) continue;
    const line = element("p", "card-snippet");
    line.append(element("i", "snippet-label", label));
    line.append(...markNodes(fragment));
    return line;
  }
  return null;
}

// matched and highlights are aligned index-for-index with the results and only
// present for a text query, so browse renders no strips and no snippets.
// The first row of a wide grid: its art loads first, the rest as it scrolls in.
const EAGER_IMAGES = 6;

// imageIsEager says whether the card art at index of a page loads eagerly.
// Only the first page's first row does: a page that "Load more" appends lands
// below the fold.
export function imageIsEager(index, page) {
  return page === 1 && index < EAGER_IMAGES;
}

// cardButtonLabel is a card button's accessible name. It starts with the
// card's name, the text the button shows when the art is missing, followed by
// the matched branches in the order their badges show them, so the visible
// text is always a run of words inside the name.
export function cardButtonLabel(card, branches = []) {
  const matched = branches.length ? ` ${branches.map(branchLabel).join(" ")} match` : "";
  return `${card.name}${matched}, from ${card.set_name}`;
}

function renderGrid(cards, { append = false, page = 1, matched, highlights } = {}) {
  const grid = $("results-grid");
  const items = cards.map((card, index) => {
    const highlight = highlights?.[index] ?? null;
    // The map stores the pair, not the card: the modal needs both, and two
    // parallel maps keyed by id are two things that can disagree.
    cardsByID.set(card.id, { card, highlight });
    const branches = cardBranches(matched, index);
    const item = document.createElement("li");
    item.className = "card-cell";

    const button = document.createElement("button");
    button.type = "button";
    button.className = "card-open";
    button.dataset.id = card.id;
    button.setAttribute("aria-label", cardButtonLabel(card, branches));

    const image = document.createElement("img");
    // Set before src, so the browser reads the priority before it fetches.
    if (!append && imageIsEager(index, page)) {
      image.loading = "eager";
      image.setAttribute("fetchpriority", "high");
    } else {
      image.loading = "lazy";
    }
    image.src = imageURL(card, "small");
    image.alt = card.name;
    image.decoding = "async";
    image.addEventListener("load", () => button.classList.add("is-loaded"), { once: true });
    // Card art is third-party and occasionally missing. Without this the cell
    // is a blank rectangle with no way to tell which card it was; the name is
    // already in the button's aria-label, so show it.
    image.addEventListener("error", () => button.classList.add("art-missing"), { once: true });
    button.append(image);
    button.append(element("span", "card-fallback-name", card.name));
    if (branches.length) button.append(badgeStrip(branches));
    item.append(button);
    // The snippet sits under the button, not inside it: the button is the
    // card's art and keeps its aspect ratio.
    const snippet = cardSnippet(highlight);
    if (snippet) item.append(snippet);
    return item;
  });

  if (append) grid.append(...items);
  else grid.replaceChildren(...items);
}

// The corpus stores pokemontcg.io art URLs, which no longer serve; scrydex
// hosts the same images keyed by card id. Anything else is passed through.
export function imageURL(card, size) {
  const source = card[`image_${size}`];
  if (source?.startsWith("https://images.pokemontcg.io/")) {
    return `https://images.scrydex.com/pokemon/${encodeURIComponent(card.id)}/${size}`;
  }
  return source;
}

function bucketMap(buckets = []) {
  return new Map(buckets.map((bucket) => [bucket.value, bucket.count]));
}

// Facet counts are disjunctive (scoped by the query plus every other active
// filter), so renderers only paint what the API returned — they never mutate
// state. Only explicit user actions may change a filter.
function renderFacets(facets = {}) {
  const supertypeCounts = bucketMap(facets.supertype);
  const supertypeAll = (facets.supertype ?? []).reduce((sum, bucket) => sum + bucket.count, 0);
  document.querySelectorAll("#supertype-toggle button").forEach((button) => {
    const labels = { pokemon: "Pokémon", trainer: "Trainer", energy: "Energy" };
    const value = button.dataset.supertype;
    const count = Number(value ? supertypeCounts.get(labels[value]) ?? 0 : supertypeAll);
    button.querySelector(".facet-count").textContent = count.toLocaleString();
    // A control already reporting zero is a one-click route into the empty
    // state, so take the click away — except on the value that is currently
    // selected, which has to stay clickable or the filter could not be undone.
    const isSelected = value === state.supertype;
    button.disabled = count === 0 && !isSelected;
  });

  const typeCounts = bucketMap(facets.types);
  document.querySelectorAll(".type-chip").forEach((button) => {
    const count = Number(typeCounts.get(button.dataset.type) ?? 0);
    button.querySelector(".facet-count").textContent = count.toLocaleString();
    const isSelected = state.types.includes(button.dataset.type);
    button.disabled = count === 0 && !isSelected;
  });

  populateSetSelect(facets.sets, state.set);
  populateFacetSelect($("rarity-select"), "All rarities", facets.rarity, state.rarity);
  populateFacetSelect($("series-select"), "All series", facets.set_series, state.series);
  syncFilterControls();
}

function populateSetSelect(buckets = [], selected) {
  const sorted = [...buckets].sort((a, b) => (a.label || a.value).localeCompare(b.label || b.value));
  setLabels.clear();
  const options = [new Option("All sets", "")];
  for (const bucket of sorted) {
    const label = bucket.label || bucket.value;
    setLabels.set(bucket.value, label);
    const option = new Option(`${label} (${Number(bucket.count).toLocaleString()})`, bucket.value);
    option.disabled = bucket.count === 0 && bucket.value !== selected;
    options.push(option);
  }
  if (selected && !sorted.some((bucket) => bucket.value === selected)) {
    options.push(new Option(`${selected} (0)`, selected));
  }
  $("set-select").replaceChildren(...options);
  $("set-select").value = selected;
}

function populateFacetSelect(select, emptyLabel, buckets = [], selected) {
  const options = [new Option(emptyLabel, "")];
  for (const bucket of buckets) {
    options.push(new Option(`${bucket.value} (${Number(bucket.count).toLocaleString()})`, bucket.value));
  }
  // A selected value can drop to zero matches under the current query and
  // other filters; keep it selectable at (0) instead of silently clearing it.
  if (selected && !buckets.some((bucket) => bucket.value === selected)) {
    options.push(new Option(`${selected} (0)`, selected));
  }
  select.replaceChildren(...options);
  select.value = selected;
}

export function syncFilterControls() {
  document.querySelectorAll("#supertype-toggle button").forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.supertype === state.supertype));
  });
  document.querySelectorAll(".type-chip").forEach((button) => {
    button.setAttribute("aria-pressed", String(state.types.includes(button.dataset.type)));
  });
  $("set-select").value = state.set;
  $("rarity-select").value = state.rarity;
  $("series-select").value = state.series;
  renderActiveFilters();
}

function renderActiveFilters() {
  const container = $("active-filters");
  const active = [];
  if (state.supertype) active.push({ label: state.supertype === "pokemon" ? "Pokémon" : titleCase(state.supertype), remove: () => { state.supertype = ""; } });
  for (const type of state.types) active.push({ label: displayType(type), remove: () => { state.types = state.types.filter((item) => item !== type); } });
  if (state.set) active.push({ label: setLabels.get(state.set) ?? state.set, remove: () => { state.set = ""; } });
  if (state.rarity) active.push({ label: state.rarity, remove: () => { state.rarity = ""; } });
  if (state.series) active.push({ label: state.series, remove: () => { state.series = ""; } });

  const chips = active.map(({ label, remove }) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "active-filter";
    button.setAttribute("aria-label", `Remove ${label} filter`);
    const text = document.createElement("span");
    text.className = "active-filter-label";
    text.textContent = label;
    button.append(text);
    const mark = document.createElement("span");
    mark.className = "remove-mark";
    mark.textContent = "×";
    mark.setAttribute("aria-hidden", "true");
    button.append(mark);
    button.addEventListener("click", () => {
      remove();
      syncFilterControls();
      onFilterChange();
    });
    return button;
  });
  container.replaceChildren(...chips);
  const hasFilters = active.length > 0;
  $("clear-filters").hidden = !hasFilters;
  // The empty card's own way out, decided by the same count the rail's control
  // uses rather than by a second reading of the state. Exactly one of the two
  // is ever offered: dropping the filters is the smaller undo, so a search
  // narrowed by both a query and filters gets that one, and only a query
  // standing alone gets the browse route. With neither, nothing narrowed the
  // search and there is nothing to offer undoing.
  $("empty-clear").hidden = !hasFilters;
  $("empty-browse").hidden = hasFilters || !queryText();
  $("stat-filters").textContent = String(active.length);
  $("filter-count").textContent = String(active.length);
  $("filter-count").hidden = active.length === 0;
}

function titleCase(value) {
  return value ? value[0].toUpperCase() + value.slice(1) : "";
}

// The corpus uses the TCG's internal names; players say Steel and Normal.
export function displayType(value) {
  if (value === "Metal") return "Steel";
  if (value === "Colorless") return "Normal";
  return value;
}

export function syncControls() {
  $("search-input").value = state.q;
  $("sort-select").querySelector('option[value="relevance"]').disabled = !queryText();
  $("sort-select").value = effectiveSort();
  const canOrder = ["hp", "name"].includes(effectiveSort());
  $("order-toggle").hidden = !canOrder;
  $("order-toggle").textContent = effectiveOrder() === "asc" ? "↑" : "↓";
  $("order-toggle").setAttribute("aria-label", `Sort ${effectiveOrder() === "asc" ? "ascending" : "descending"}; toggle order`);
}

function makeSkeleton() {
  const item = document.createElement("li");
  item.className = "card-cell skeleton-card";
  item.setAttribute("aria-hidden", "true");
  item.append(document.createElement("span"), document.createElement("i"));
  return item;
}

// A stale grid is one whose cards outlived the search that produced them: the
// last request failed, so what is on screen answers an older query. It is not
// busy in the loading sense and it is not settled either, and of the two
// aria-busy can only say one — so it says "true". Announcing a failed search's
// leftovers as the finished answer is the worse lie, and the state ends the
// moment a search succeeds.
export function setStale(isStale) {
  const grid = $("results-grid");
  grid.classList.toggle("is-stale", isStale);
  if (isStale) {
    $("total-count").textContent = countLineText({
      phase: "ended",
      cardsShown: grid.querySelectorAll(".card-open").length,
      current: $("total-count").textContent,
    });
  }
  if (legendHiddenWhenStale({ stale: isStale, badgedCards: grid.querySelectorAll(".match-badges").length })) {
    $("badge-legend").hidden = true;
  }
  grid.setAttribute("aria-busy", String(isStale || grid.classList.contains("is-loading")));
}

// countLineText decides what the count line above the grid says. While a
// search is on its way it keeps what it says (the markup's "Loading cards…"
// before the first answer, the last count after that). A search that answered
// writes its total. One that failed or was refused leaves the count of the
// cards still on screen, or a dash when there are none, so the line never
// goes on saying it is loading once nothing is.
export function countLineText({ phase, total, cardsShown = 0, current = "" }) {
  if (phase === "done") return `${Number(total).toLocaleString()} ${total === 1 ? "card" : "cards"}`;
  if (phase === "ended" && !(cardsShown > 0)) return "—";
  return current;
}

// legendHiddenWhenStale decides whether the badge legend goes after a search
// that failed or was refused: it does when no card left on screen carries a
// badge (the line was only held for results that never came), and stays over
// the previous search's badged cards.
export function legendHiddenWhenStale({ stale, badgedCards }) {
  return Boolean(stale) && !(badgedCards > 0);
}

// skeletonCount is how many placeholder cards a loading first page draws: one
// per card the request asks for, so the grid keeps its height when they land.
export function skeletonCount(pageSize) {
  return Number.isInteger(pageSize) && pageSize > 0 ? pageSize : 0;
}

export function setLoading(loading, { append = false, pageSize = 0 } = {}) {
  const grid = $("results-grid");
  // Clearing the loading flag must not clear staleness with it: the request
  // that finished is the one that failed, and the cards under it are still the
  // previous query's.
  grid.setAttribute("aria-busy", String(loading || grid.classList.contains("is-stale")));
  if (loading) {
    $("load-more").disabled = true;
    if (!append && !grid.querySelector(".card-open")) {
      grid.hidden = false;
      grid.replaceChildren(...Array.from({ length: skeletonCount(pageSize) }, makeSkeleton));
    }
    grid.classList.add("is-loading");
    return;
  }
  grid.classList.remove("is-loading");
  $("load-more").disabled = false;
}
