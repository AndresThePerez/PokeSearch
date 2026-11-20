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
// How many times the list has been closed. A suggest answer carries the count
// from when it was asked for, so one asked for before a close cannot reopen
// the list after it.
let closes = 0;

// suggestAnswerRenders decides whether a suggest answer may open the list:
// only while the box has focus, only when the list has not been closed since
// the request was made, and only when the answer is for the text the box
// holds now.
export function suggestAnswerRenders({ focused, requestedAt, closes: closedNow, requestedText, currentText }) {
  return Boolean(focused) && requestedAt === closedNow && requestedText === currentText;
}

// composingKey says whether a key press belongs to an input method rather
// than to the page: the Enter that confirms a Japanese, Chinese or Korean
// word, and the arrows and Escape that move through or cancel its
// candidates. Chrome and Safari report some of those as keyCode 229 with
// isComposing already false.
export function composingKey(event) {
  return Boolean(event.isComposing) || event.keyCode === 229;
}

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
  suggestTimer = null;
  const controller = new AbortController();
  suggestController = controller;
  const requestedAt = closes;
  const requestedText = queryText();
  try {
    const names = await fetchSuggestions(requestedText, controller.signal);
    const renders = suggestAnswerRenders({
      focused: document.activeElement === $("search-input"),
      requestedAt,
      closes,
      requestedText,
      currentText: queryText(),
    });
    if (renders) renderSuggestions(names);
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

// Closing the list also stops what would reopen it: the timer waiting to ask
// and the request already asked. Only typing in the box asks again.
export function closeSuggestions() {
  closes += 1;
  clearTimeout(suggestTimer);
  suggestTimer = null;
  suggestController?.abort();
  suggestController = null;
  suggestions = [];
  activeSuggestion = -1;
  $("suggest-list").hidden = true;
  $("suggest-list").replaceChildren();
  $("search-input").closest("[role=combobox]").setAttribute("aria-expanded", "false");
  $("search-input").removeAttribute("aria-activedescendant");
}

// enterEffects decides what Enter in the search box does, as the list of
// steps to take in order. An Enter that confirms an input-method composition
// is the input method's, so it does nothing here. A highlighted option is
// picked, which is a search of its own. Otherwise the list closes and the typed query is committed:
// that runs the keystroke search still waiting on the debounce, once, when
// the query differs from the one the address (and so the grid) holds, and
// writes no history and searches nothing when it does not. In that second
// case a keystroke search still waiting would only repeat the results on
// screen, so it is dropped. Either way focus then moves to what the search
// found (see enterFocusTarget).
export function enterEffects({ highlighted, pending, changed, composing }) {
  if (composing) return [];
  if (highlighted) return ["pick"];
  if (!changed && pending) return ["close", "drop", "commit", "focus"];
  return ["close", "commit", "focus"];
}

// enterFocusTarget decides where focus goes once the search Enter asked for
// has answered: the grid when it found cards, the empty-state message when it
// found none, and nowhere (focus stays in the box, beside the notice or the
// banner) when it could not run or a newer search replaced it. The move
// belongs to that Enter and to the text the box held then, so it is dropped
// when the visitor has typed in the box since, or pressed Enter again (the
// later Enter makes its own move). Focus the visitor has already moved
// elsewhere is left alone.
export function enterFocusTarget({ outcome, inBox, typedSinceEnter = false, superseded = false }) {
  if (!inBox || typedSinceEnter || superseded) return null;
  if (outcome === "results") return "results-grid";
  if (outcome === "none") return "empty-message";
  return null;
}

// shownOutcome reads the outcome an Enter that ran no search already has on
// screen: cards, the empty state, or neither (the last search failed).
export function shownOutcome({ gridHidden, emptyHidden }) {
  if (!gridHidden) return "results";
  if (!emptyHidden) return "none";
  return "failed";
}

// The Enters that asked for a focus move, and the input events the box has
// had, counted so that an answer can tell whether its Enter is still the
// latest and whether the visitor has typed since.
let enters = 0;
let inputs = 0;

// noteSearchBoxInput is called on every input event in the box: a key, a
// deletion or a paste.
export function noteSearchBoxInput() {
  inputs += 1;
}

// blurCloses decides whether the list's delayed close after the box loses
// focus still runs: not when the box has focus again by then, or it would
// cancel the suggestions the visitor's next keystroke just asked for.
export function blurCloses({ focused }) {
  return !focused;
}

// focusAfterEnter waits for the search Enter started, if it started one, and
// then moves focus. The grid and the empty-state message both take focus
// from script only (tabindex="-1"), and scroll-padding-top keeps them clear
// of the sticky header when focus scrolls them into view.
async function focusAfterEnter(search) {
  enters += 1;
  const enter = enters;
  const inputsAtEnter = inputs;
  const textAtEnter = $("search-input").value;
  let outcome;
  if (search) {
    try {
      outcome = await search;
    } catch {
      // A search that threw before it could report has nothing to show.
      return;
    }
  } else {
    outcome = shownOutcome({ gridHidden: $("results-grid").hidden, emptyHidden: $("empty-state").hidden });
  }
  const target = enterFocusTarget({
    outcome,
    inBox: document.activeElement === $("search-input"),
    typedSinceEnter: inputs !== inputsAtEnter || $("search-input").value !== textAtEnter,
    superseded: enter !== enters,
  });
  if (target) document.getElementById(target).focus();
}

function handleEnter(event) {
  const effects = enterEffects({
    highlighted: activeSuggestion >= 0,
    composing: composingKey(event),
    ...searchBoxStatus(),
  });
  if (effects.includes("pick")) event.preventDefault();
  let search = null;
  for (const step of effects) {
    if (step === "pick") pickSuggestion(activeSuggestion);
    else if (step === "close") closeSuggestions();
    else if (step === "drop") cancelScheduledSearch();
    else if (step === "commit") search = commitQuery("enter");
    else if (step === "focus") focusAfterEnter(search);
  }
}

export function handleSuggestKeydown(event) {
  if (event.key === "Enter") {
    handleEnter(event);
    return;
  }
  // Arrows and Escape during a composition move through or cancel the input
  // method's candidates; taking them would break that.
  if (composingKey(event)) return;
  if (event.key === "ArrowDown" && suggestions.length) {
    event.preventDefault();
    setActiveSuggestion(activeSuggestion + 1);
  } else if (event.key === "ArrowUp" && suggestions.length) {
    event.preventDefault();
    setActiveSuggestion(activeSuggestion - 1);
  } else if (event.key === "Escape") {
    // Only swallow Escape when the list is open, so it still closes the modal.
    if (!$("suggest-list").hidden) event.preventDefault();
    closeSuggestions();
  }
}
