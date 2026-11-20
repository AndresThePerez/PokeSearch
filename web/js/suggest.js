// The autocomplete listbox: debounce, in-flight cancellation, the rendered
// options, and the ARIA combobox keyboard contract (ArrowUp/Down wrap,
// Enter commits, Escape closes).

import { $ } from "./util.js";
import { state, queryText } from "./state.js";
import { fetchSuggestions, runSearch, cancelScheduledSearch, commitQuery, searchBoxStatus } from "./api.js";

let suggestController = null;
let suggestTimer = null;
let suggestions = [];
let activeSuggestion = -1;

export function scheduleSuggest() {
  clearTimeout(suggestTimer);
  suggestController?.abort();
  if (!queryText()) {
    closeSuggestions();
    return;
  }
  suggestTimer = setTimeout(loadSuggestions, 150);
}

async function loadSuggestions() {
  const controller = new AbortController();
  suggestController = controller;
  try {
    const names = await fetchSuggestions(queryText(), controller.signal);
    renderSuggestions(names);
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
    // mousedown, not click: the input's blur handler would close the list
    // before a click could ever land.
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
  cancelScheduledSearch();
  state.q = name;
  state.sort = "";
  state.order = "";
  $("search-input").value = name;
  closeSuggestions();
  // Picking a completion is its own search, so Back returns to the results the
  // typed prefix had produced.
  runSearch({ push: true });
}

export function closeSuggestions() {
  suggestions = [];
  activeSuggestion = -1;
  $("suggest-list").hidden = true;
  $("suggest-list").replaceChildren();
  $("search-input").closest("[role=combobox]").setAttribute("aria-expanded", "false");
  $("search-input").removeAttribute("aria-activedescendant");
}

// enterEffects decides what Enter in the search box does, as the list of
// steps to take in order. A highlighted option is picked, which is a search
// of its own. Otherwise the list closes and the typed query is committed:
// that runs the keystroke search still waiting on the debounce, once, when
// the query differs from the one the address (and so the grid) holds, and
// writes no history and searches nothing when it does not. In that second
// case a keystroke search still waiting would only repeat the results on
// screen, so it is dropped. Either way focus moves to the results, which is
// where Enter was asking to go.
export function enterEffects({ highlighted, pending, changed }) {
  if (highlighted) return ["pick"];
  if (!changed && pending) return ["close", "drop", "commit", "focus"];
  return ["close", "commit", "focus"];
}

const ENTER_STEPS = {
  pick: () => pickSuggestion(activeSuggestion),
  close: closeSuggestions,
  drop: cancelScheduledSearch,
  commit: () => commitQuery("enter"),
  // The grid is focusable (tabindex="-1"), and scroll-padding-top keeps it
  // clear of the sticky header when focus scrolls it into view.
  focus: () => $("results-grid").focus(),
};

export function handleSuggestKeydown(event) {
  if (event.key === "ArrowDown" && suggestions.length) {
    event.preventDefault();
    setActiveSuggestion(activeSuggestion + 1);
  } else if (event.key === "ArrowUp" && suggestions.length) {
    event.preventDefault();
    setActiveSuggestion(activeSuggestion - 1);
  } else if (event.key === "Enter") {
    const highlighted = activeSuggestion >= 0;
    if (highlighted) event.preventDefault();
    for (const step of enterEffects({ highlighted, ...searchBoxStatus() })) ENTER_STEPS[step]();
  } else if (event.key === "Escape") {
    // Only swallow Escape when the list is open, so it still closes the modal.
    if (!$("suggest-list").hidden) event.preventDefault();
    closeSuggestions();
  }
}
