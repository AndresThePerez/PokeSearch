// Entry point: wires the modules to the markup and starts the first search.
// Every DOM event listener that is not owned by a specific module lives here.

import { $ } from "./util.js";
import { state, readStateFromURL, effectiveOrder } from "./state.js";
import {
  syncControls,
  syncFilterControls,
  setFilterChangeHandler,
} from "./render.js";
import { runSearch, scheduleSearch, refreshHealth } from "./api.js";
import { scheduleSuggest, closeSuggestions, handleSuggestKeydown } from "./suggest.js";
import { bindModalEvents, openDeepLink } from "./modal.js";

function bindCoreEvents() {
  $("search-input").addEventListener("input", (event) => {
    state.q = event.target.value;
    // A new query invalidates an explicit sort: relevance is only meaningful
    // with a query, and the server would flip it anyway.
    state.sort = "";
    state.order = "";
    syncControls();
    scheduleSearch();
    scheduleSuggest();
  });

  $("search-input").addEventListener("keydown", handleSuggestKeydown);
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

  $("copy-response").addEventListener("click", async () => {
    await navigator.clipboard.writeText($("response-json").textContent);
    $("copy-response").textContent = "Copied";
    setTimeout(() => { $("copy-response").textContent = "Copy response"; }, 1200);
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

  $("set-select").addEventListener("change", (event) => {
    state.set = event.target.value;
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
    state.set = "";
    state.rarity = "";
    state.series = "";
    syncFilterControls();
    runSearch();
  });

  $("filter-toggle").addEventListener("click", () => {
    const expanded = $("filter-toggle").getAttribute("aria-expanded") === "true";
    $("filter-toggle").setAttribute("aria-expanded", String(!expanded));
  });

  $("filter-done").addEventListener("click", () => {
    $("filter-toggle").setAttribute("aria-expanded", "false");
    $("filter-toggle").scrollIntoView({ block: "nearest" });
    $("filter-toggle").focus({ preventScroll: true });
  });
}

function init() {
  readStateFromURL();
  if (window.matchMedia("(max-width: 768px)").matches) {
    $("query-inspector").open = false;
    $("response-inspector").open = false;
  }
  // render.js cannot import api.js without making the graph cyclic, so the
  // active-filter chips get their search call injected here.
  setFilterChangeHandler(() => runSearch());
  syncControls();
  syncFilterControls();
  bindCoreEvents();
  bindFilterEvents();
  bindModalEvents();
  refreshHealth();
  runSearch().then(openDeepLink);
}

init();
