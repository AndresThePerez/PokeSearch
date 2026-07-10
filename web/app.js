"use strict";

const $ = (id) => document.getElementById(id);

const defaults = { q: "", supertype: "", types: [], rarity: "", series: "", sort: "", order: "", page: 1 };
let state = { ...defaults };
let searchController = null;
let debounceTimer = null;
let suggestController = null;
let suggestTimer = null;
let suggestions = [];
let activeSuggestion = -1;
const cardsByID = new Map();

function readStateFromURL() {
  const sp = new URLSearchParams(location.search);
  state = { ...defaults };
  state.q = sp.get("q") ?? "";
  state.supertype = sp.get("supertype") ?? "";
  state.types = (sp.get("types") ?? "").split(",").filter(Boolean);
  state.rarity = sp.get("rarity") ?? "";
  state.series = sp.get("series") ?? "";
  state.sort = sp.get("sort") ?? "";
  state.order = sp.get("order") ?? "";
}

function writeStateToURL() {
  const sp = new URLSearchParams();
  if (state.q) sp.set("q", state.q);
  if (state.supertype) sp.set("supertype", state.supertype);
  if (state.types.length) sp.set("types", state.types.join(","));
  if (state.rarity) sp.set("rarity", state.rarity);
  if (state.series) sp.set("series", state.series);
  if (state.sort) sp.set("sort", state.sort);
  if (state.order) sp.set("order", state.order);
  const qs = sp.toString();
  history.replaceState(null, "", `${location.pathname}${qs ? `?${qs}` : ""}${location.hash}`);
}

function searchParams() {
  const sp = new URLSearchParams();
  if (state.q) sp.set("q", state.q);
  if (state.supertype) sp.set("supertype", state.supertype);
  if (state.types.length) sp.set("types", state.types.join(","));
  if (state.rarity) sp.set("rarity", state.rarity);
  if (state.series) sp.set("series", state.series);
  if (state.sort) sp.set("sort", state.sort);
  if (state.order) sp.set("order", state.order);
  if (state.page > 1) sp.set("page", String(state.page));
  sp.set("debug", "1");
  return sp;
}

async function runSearch({ append = false } = {}) {
  if (!append) state.page = 1;
  writeStateToURL();
  searchController?.abort();
  const controller = new AbortController();
  searchController = controller;
  setLoading(true, { append });
  try {
    const res = await fetch(`/api/search?${searchParams()}`, { signal: controller.signal });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    renderResults(data, { append });
    renderInspector(data.dsl);
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

function scheduleSearch() {
  clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => runSearch(), 200);
}

function effectiveSort() {
  return state.sort || (state.q ? "relevance" : "newest");
}

function effectiveOrder() {
  if (state.order) return state.order;
  if (effectiveSort() === "hp") return "desc";
  if (effectiveSort() === "name") return "asc";
  return "";
}

function syncControls() {
  $("search-input").value = state.q;
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

function setLoading(loading, { append = false } = {}) {
  const grid = $("results-grid");
  grid.setAttribute("aria-busy", String(loading));
  if (loading) {
    $("load-more").disabled = true;
    if (!append) {
      grid.replaceChildren(...Array.from({ length: 10 }, makeSkeleton));
    }
    return;
  }
  $("load-more").disabled = false;
}

function renderResults(data, { append = false } = {}) {
  state.page = data.page;
  renderGrid(data.results, { append });
  renderFacets(data.facets, data.total);
  $("total-count").textContent = `${Number(data.total).toLocaleString()} ${data.total === 1 ? "card" : "cards"}`;
  $("load-more").hidden = data.pages === 0 || data.page >= data.pages;
  $("empty-state").hidden = data.total !== 0;
  $("results-grid").hidden = data.total === 0;
  syncControls();
}

function renderGrid(cards, { append = false } = {}) {
  const grid = $("results-grid");
  const items = cards.map((card) => {
    cardsByID.set(card.id, card);
    const item = document.createElement("li");
    item.className = "card-cell";

    const button = document.createElement("button");
    button.type = "button";
    button.className = "card-open";
    button.dataset.id = card.id;
    button.setAttribute("aria-label", `Open ${card.name} from ${card.set_name}`);

    const image = document.createElement("img");
    image.src = imageURL(card, "small");
    image.alt = card.name;
    image.loading = "lazy";
    image.decoding = "async";
    image.addEventListener("load", () => button.classList.add("is-loaded"), { once: true });
    button.append(image);
    item.append(button);
    return item;
  });

  if (append) grid.append(...items);
  else grid.replaceChildren(...items);
}

function imageURL(card, size) {
  const source = card[`image_${size}`];
  if (source?.startsWith("https://images.pokemontcg.io/")) {
    return `https://images.scrydex.com/pokemon/${encodeURIComponent(card.id)}/${size}`;
  }
  return source;
}

function bucketMap(buckets = []) {
  return new Map(buckets.map((bucket) => [bucket.value, bucket.count]));
}

function renderFacets(facets = {}, total = 0) {
  const supertypeCounts = bucketMap(facets.supertype);
  document.querySelectorAll("#supertype-toggle button").forEach((button) => {
    const labels = { pokemon: "Pokémon", trainer: "Trainer", energy: "Energy" };
    const value = button.dataset.supertype;
    button.querySelector(".facet-count").textContent = Number(value ? supertypeCounts.get(labels[value]) ?? 0 : total).toLocaleString();
  });

  const typeCounts = bucketMap(facets.types);
  document.querySelectorAll(".type-chip").forEach((button) => {
    button.querySelector(".facet-count").textContent = Number(typeCounts.get(button.dataset.type) ?? 0).toLocaleString();
  });

  populateFacetSelect($("rarity-select"), "All rarities", facets.rarity, state.rarity, (value) => { state.rarity = value; });
  populateFacetSelect($("series-select"), "All series", facets.set_series, state.series, (value) => { state.series = value; });
  syncFilterControls();
}

function populateFacetSelect(select, emptyLabel, buckets = [], selected, onMissing) {
  const options = [new Option(emptyLabel, "")];
  for (const bucket of buckets) {
    options.push(new Option(`${bucket.value} (${Number(bucket.count).toLocaleString()})`, bucket.value));
  }
  select.replaceChildren(...options);
  if (selected && buckets.some((bucket) => bucket.value === selected)) {
    select.value = selected;
  } else {
    select.value = "";
    if (selected) onMissing("");
  }
}

function syncFilterControls() {
  document.querySelectorAll("#supertype-toggle button").forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.supertype === state.supertype));
  });
  document.querySelectorAll(".type-chip").forEach((button) => {
    button.setAttribute("aria-pressed", String(state.types.includes(button.dataset.type)));
  });
  $("rarity-select").value = state.rarity;
  $("series-select").value = state.series;
  renderActiveFilters();
}

function renderActiveFilters() {
  const container = $("active-filters");
  const active = [];
  if (state.supertype) active.push({ label: state.supertype === "pokemon" ? "Pokémon" : titleCase(state.supertype), remove: () => { state.supertype = ""; } });
  for (const type of state.types) active.push({ label: type, remove: () => { state.types = state.types.filter((item) => item !== type); } });
  if (state.rarity) active.push({ label: state.rarity, remove: () => { state.rarity = ""; } });
  if (state.series) active.push({ label: state.series, remove: () => { state.series = ""; } });

  const chips = active.map(({ label, remove }) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "active-filter";
    button.setAttribute("aria-label", `Remove ${label} filter`);
    button.append(document.createTextNode(label));
    const mark = document.createElement("span");
    mark.textContent = "×";
    mark.setAttribute("aria-hidden", "true");
    button.append(mark);
    button.addEventListener("click", () => {
      remove();
      syncFilterControls();
      runSearch();
    });
    return button;
  });
  container.replaceChildren(...chips);
  $("clear-filters").hidden = active.length === 0;
}

function titleCase(value) {
  return value ? value[0].toUpperCase() + value.slice(1) : "";
}

function scheduleSuggest() {
  clearTimeout(suggestTimer);
  suggestController?.abort();
  if (!state.q) {
    closeSuggestions();
    return;
  }
  suggestTimer = setTimeout(fetchSuggestions, 150);
}

async function fetchSuggestions() {
  const controller = new AbortController();
  suggestController = controller;
  try {
    const sp = new URLSearchParams({ q: state.q, debug: "1" });
    const res = await fetch(`/api/suggest?${sp}`, { signal: controller.signal });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    renderSuggestions(data.suggestions.slice(0, 8));
  } catch (err) {
    if (err.name !== "AbortError") closeSuggestions();
  }
}

function renderSuggestions(names) {
  const list = $("suggest-list");
  suggestions = [...new Set(names)];
  activeSuggestion = -1;
  const options = suggestions.map((name, index) => {
    const option = document.createElement("li");
    option.id = `suggest-opt-${index}`;
    option.role = "option";
    option.setAttribute("aria-selected", "false");
    option.textContent = name;
    option.addEventListener("mousedown", (event) => {
      event.preventDefault();
      pickSuggestion(index);
    });
    return option;
  });
  list.replaceChildren(...options);
  const isOpen = options.length > 0;
  list.hidden = !isOpen;
  $("search-input").closest("[role=combobox]").setAttribute("aria-expanded", String(isOpen));
  $("search-input").removeAttribute("aria-activedescendant");
}

function setActiveSuggestion(index) {
  if (!suggestions.length) return;
  activeSuggestion = (index + suggestions.length) % suggestions.length;
  $("suggest-list").querySelectorAll("[role=option]").forEach((option, optionIndex) => {
    option.setAttribute("aria-selected", String(optionIndex === activeSuggestion));
  });
  const activeID = `suggest-opt-${activeSuggestion}`;
  $("search-input").setAttribute("aria-activedescendant", activeID);
  document.getElementById(activeID)?.scrollIntoView({ block: "nearest" });
}

function pickSuggestion(index) {
  const name = suggestions[index];
  if (!name) return;
  clearTimeout(debounceTimer);
  state.q = name;
  state.sort = "";
  state.order = "";
  $("search-input").value = name;
  closeSuggestions();
  runSearch();
}

function closeSuggestions() {
  suggestions = [];
  activeSuggestion = -1;
  $("suggest-list").hidden = true;
  $("suggest-list").replaceChildren();
  $("search-input").closest("[role=combobox]").setAttribute("aria-expanded", "false");
  $("search-input").removeAttribute("aria-activedescendant");
}

function renderInspector(dsl) {
  $("dsl-json").textContent = dsl ? JSON.stringify(dsl, null, 2) : "No query available.";
}

function setDegraded(isDegraded) {
  $("degraded-banner").hidden = !isDegraded;
}

function bindCoreEvents() {
  $("search-input").addEventListener("input", (event) => {
    state.q = event.target.value.trim();
    state.sort = "";
    state.order = "";
    syncControls();
    scheduleSearch();
    scheduleSuggest();
  });

  $("search-input").addEventListener("keydown", (event) => {
    if (event.key === "ArrowDown" && suggestions.length) {
      event.preventDefault();
      setActiveSuggestion(activeSuggestion + 1);
    } else if (event.key === "ArrowUp" && suggestions.length) {
      event.preventDefault();
      setActiveSuggestion(activeSuggestion - 1);
    } else if (event.key === "Enter" && activeSuggestion >= 0) {
      event.preventDefault();
      pickSuggestion(activeSuggestion);
    } else if (event.key === "Escape") {
      if (!$("suggest-list").hidden) event.preventDefault();
      closeSuggestions();
    }
  });

  $("search-input").addEventListener("blur", () => setTimeout(closeSuggestions, 100));

  $("sort-select").addEventListener("change", (event) => {
    state.sort = event.target.value;
    state.order = "";
    syncControls();
    runSearch();
  });

  $("order-toggle").addEventListener("click", () => {
    state.order = effectiveOrder() === "asc" ? "desc" : "asc";
    syncControls();
    runSearch();
  });

  $("load-more").addEventListener("click", () => {
    state.page += 1;
    runSearch({ append: true });
  });

  $("copy-dsl").addEventListener("click", async () => {
    await navigator.clipboard.writeText($("dsl-json").textContent);
    $("copy-dsl").textContent = "Copied";
    setTimeout(() => { $("copy-dsl").textContent = "Copy DSL"; }, 1200);
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "/" && document.activeElement !== $("search-input")) {
      event.preventDefault();
      $("search-input").focus();
    }
  });
}

function bindFilterEvents() {
  document.querySelectorAll("#supertype-toggle button").forEach((button) => {
    button.addEventListener("click", () => {
      state.supertype = button.dataset.supertype;
      syncFilterControls();
      runSearch();
    });
  });

  document.querySelectorAll(".type-chip").forEach((button) => {
    button.addEventListener("click", () => {
      const type = button.dataset.type;
      state.types = state.types.includes(type) ? state.types.filter((item) => item !== type) : [...state.types, type];
      syncFilterControls();
      runSearch();
    });
  });

  $("rarity-select").addEventListener("change", (event) => {
    state.rarity = event.target.value;
    syncFilterControls();
    runSearch();
  });

  $("series-select").addEventListener("change", (event) => {
    state.series = event.target.value;
    syncFilterControls();
    runSearch();
  });

  $("clear-filters").addEventListener("click", () => {
    state.supertype = "";
    state.types = [];
    state.rarity = "";
    state.series = "";
    syncFilterControls();
    runSearch();
  });

  $("filter-toggle").addEventListener("click", () => {
    const expanded = $("filter-toggle").getAttribute("aria-expanded") === "true";
    $("filter-toggle").setAttribute("aria-expanded", String(!expanded));
  });
}

function init() {
  readStateFromURL();
  syncControls();
  syncFilterControls();
  bindCoreEvents();
  bindFilterEvents();
  runSearch();
}

init();
