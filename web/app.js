"use strict";

const $ = (id) => document.getElementById(id);

const defaults = { q: "", supertype: "", types: [], rarity: "", series: "", sort: "", order: "", page: 1 };
let state = { ...defaults };
let searchController = null;
let debounceTimer = null;
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
  renderFacets(data.facets);
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

function renderFacets() {
  // Task 12 wires facet counts and controls into this response hook.
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
  });

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

function init() {
  readStateFromURL();
  syncControls();
  bindCoreEvents();
  runSearch();
}

init();
