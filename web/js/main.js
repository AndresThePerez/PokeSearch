// Entry point: wires the modules to the markup and starts the first search.
// Every DOM event listener that is not owned by a specific module lives here.

import { $ } from "./util.js";
import { state, readStateFromURL, effectiveOrder, buildParams } from "./state.js";
import {
  syncControls,
  syncFilterControls,
  setFilterChangeHandler,
} from "./render.js";
import { runSearch, scheduleSearch, refreshHealth } from "./api.js";
import { scheduleSuggest, closeSuggestions, handleSuggestKeydown } from "./suggest.js";
import { bindModalEvents, openDeepLink } from "./modal.js";
import { lastResponseHadDSL } from "./telemetry.js";

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
    runSearch({ push: true });
  });

  $("order-toggle").addEventListener("click", () => {
    state.order = effectiveOrder() === "asc" ? "desc" : "asc";
    syncControls();
    runSearch({ push: true });
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

  // The DSL only comes back when the panel is open, so opening it after a
  // search has to fetch once to fill it. Closing fires this too — hence the
  // open check, which also stops the re-fetch looping.
  // Taking a correction is a search the user asked for, so it earns a history
  // entry — Back returns to what they actually typed.
  $("did-you-mean").addEventListener("click", (event) => {
    const suggestion = event.currentTarget.dataset.suggestion;
    if (!suggestion) return;
    state.q = suggestion;
    state.sort = "";
    state.order = "";
    syncControls();
    runSearch({ push: true });
  });

  $("query-inspector").addEventListener("toggle", () => {
    if ($("query-inspector").open && !lastResponseHadDSL()) runSearch();
  });

  // Back and Forward restore a previous search. A history entry that only
  // differs by the #card= hash is the modal's, not a search's, and re-running
  // the identical query for it would be pure waste.
  window.addEventListener("popstate", () => {
    const before = buildParams().toString();
    readStateFromURL();
    syncControls();
    syncFilterControls();
    if (buildParams().toString() !== before) runSearch({ fromHistory: true });
  });
}

function bindFilterEvents() {
  document.querySelectorAll("#supertype-toggle button").forEach((button) => {
    button.addEventListener("click", () => {
      state.supertype = button.dataset.supertype;
      syncFilterControls();
      runSearch({ push: true });
    });
  });

  document.querySelectorAll(".type-chip").forEach((button) => {
    button.addEventListener("click", () => {
      const type = button.dataset.type;
      state.types = state.types.includes(type) ? state.types.filter((item) => item !== type) : [...state.types, type];
      syncFilterControls();
      runSearch({ push: true });
    });
  });

  $("rarity-select").addEventListener("change", (event) => {
    state.rarity = event.target.value;
    syncFilterControls();
    runSearch({ push: true });
  });

  $("set-select").addEventListener("change", (event) => {
    state.set = event.target.value;
    syncFilterControls();
    runSearch({ push: true });
  });

  $("series-select").addEventListener("change", (event) => {
    state.series = event.target.value;
    syncFilterControls();
    runSearch({ push: true });
  });

  $("clear-filters").addEventListener("click", () => {
    state.supertype = "";
    state.types = [];
    state.set = "";
    state.rarity = "";
    state.series = "";
    syncFilterControls();
    runSearch({ push: true });
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
  setFilterChangeHandler(() => runSearch({ push: true }));
  syncControls();
  syncFilterControls();
  bindCoreEvents();
  bindFilterEvents();
  bindModalEvents();
  refreshHealth();
  runSearch().then(openDeepLink);
}

init();
