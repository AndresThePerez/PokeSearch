// Everything that paints a response: the card grid, the facet controls, the
// active-filter chips, the loading skeleton and the sort control.
//
// This module must not import api.js, or the graph becomes cyclic. The one
// thing it needs from there — "the user removed a filter, search again" — is
// injected by main.js through setFilterChangeHandler.

import { $, element } from "./util.js";
import { state, queryText, effectiveSort, effectiveOrder } from "./state.js";
import { renderStats } from "./telemetry.js";

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

export function renderResults(data, { append = false, roundTripMs = 0 } = {}) {
  state.page = data.page;
  renderGrid(data.results, { append, matched: data.matched, highlights: data.highlights });
  renderFacets(data.facets);
  $("total-count").textContent = `${Number(data.total).toLocaleString()} ${data.total === 1 ? "card" : "cards"}`;
  $("load-more").hidden = data.pages === 0 || data.page >= data.pages;
  $("empty-state").hidden = data.total !== 0;
  $("results-grid").hidden = data.total === 0;
  renderStats(data, roundTripMs);
  syncControls();
}

// The four relevance branches, in the order the server reports them, mapped to
// what fits on a badge. Keys are search.Branches' names — they arrive from the
// API, so a new branch shows up under its raw name rather than vanishing.
const BRANCH_LABELS = {
  exact: "exact",
  prefix: "prefix",
  "fuzzy-name": "fuzzy",
  text: "text",
};

export function branchLabel(name) {
  return BRANCH_LABELS[name] ?? name;
}

// badgeStrip renders which relevance clauses ES says matched this card. It is
// aria-hidden because the same information goes into the button's accessible
// name as a sentence — four loose words read out per card is not an
// improvement over one.
function badgeStrip(names) {
  const strip = element("span", "match-badges");
  strip.setAttribute("aria-hidden", "true");
  for (const name of names) {
    strip.append(element("i", `match-badge match-${name}`, branchLabel(name)));
  }
  return strip;
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
    const at = original.indexOf(plain);
    if (at === -1 || !plain) continue;
    const nodes = [];
    if (at > 0) nodes.push(document.createTextNode(original.slice(0, at)));
    nodes.push(...markNodes(fragment));
    const end = at + plain.length;
    if (end < original.length) nodes.push(document.createTextNode(original.slice(end)));
    return nodes;
  }
  return null;
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
function renderGrid(cards, { append = false, matched, highlights } = {}) {
  const grid = $("results-grid");
  const items = cards.map((card, index) => {
    const highlight = highlights?.[index] ?? null;
    // The map stores the pair, not the card: the modal needs both, and two
    // parallel maps keyed by id are two things that can disagree.
    cardsByID.set(card.id, { card, highlight });
    const branches = matched?.[index] ?? [];
    const item = document.createElement("li");
    item.className = "card-cell";

    const button = document.createElement("button");
    button.type = "button";
    button.className = "card-open";
    button.dataset.id = card.id;
    const label = `Open ${card.name} from ${card.set_name}`;
    button.setAttribute("aria-label", branches.length
      ? `${label}. Matched ${branches.map(branchLabel).join(", ")}`
      : label);

    const image = document.createElement("img");
    image.src = imageURL(card, "small");
    image.alt = card.name;
    image.loading = "lazy";
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
    button.querySelector(".facet-count").textContent = Number(value ? supertypeCounts.get(labels[value]) ?? 0 : supertypeAll).toLocaleString();
  });

  const typeCounts = bucketMap(facets.types);
  document.querySelectorAll(".type-chip").forEach((button) => {
    button.querySelector(".facet-count").textContent = Number(typeCounts.get(button.dataset.type) ?? 0).toLocaleString();
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
  $("clear-filters").hidden = active.length === 0;
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

export function setLoading(loading, { append = false } = {}) {
  const grid = $("results-grid");
  grid.setAttribute("aria-busy", String(loading));
  if (loading) {
    $("load-more").disabled = true;
    if (!append && !grid.querySelector(".card-open")) {
      grid.hidden = false;
      grid.replaceChildren(...Array.from({ length: 10 }, makeSkeleton));
    }
    grid.classList.add("is-loading");
    return;
  }
  grid.classList.remove("is-loading");
  $("load-more").disabled = false;
}
