// Entry point: wires the modules to the markup and starts the first search.
// Every DOM event listener that is not owned by a specific module lives here.

import { $ } from "./util.js";
import { state, readStateFromURL, effectiveOrder, buildParams } from "./state.js";
import {
  syncControls,
  syncFilterControls,
  setFilterChangeHandler,
  renderExamples,
  renderGuide,
} from "./render.js";
import {
  runSearch,
  scheduleSearch,
  refreshHealth,
  commitQuery,
  hideNotice,
  isOwnStepBack,
  noteHistoryNavigation,
  popstateRunsSearch,
  queryPanelRequest,
  refreshQuery,
} from "./api.js";
import {
  scheduleSuggest,
  closeSuggestions,
  handleSuggestKeydown,
  noteSearchBoxInput,
  blurCloses,
  enterFocusTarget,
  shownOutcome,
} from "./suggest.js";
import { bindModalEvents, openDeepLink, syncDialogToAddress } from "./modal.js";
import { lastResponseHadDSL, refreshRankingLab, loadArchiveCounts, noteSearchLoading, setStripCompact } from "./telemetry.js";
import { showStats } from "./stats.js";

// The app has two views and one hash router. #stats is a route; #card= is not
// — the modal is an overlay on whichever view is underneath it, so a card deep
// link must never move the reader off the Stats view and back again.
const STATS_ROUTE = "#stats";

function isStatsRoute() {
  return location.hash === STATS_ROUTE;
}

// statsViewOpen asks what is actually on screen rather than what the hash
// says, because a #card= overlay leaves the route underneath it untouched.
function statsViewOpen() {
  return document.body.classList.contains("route-stats");
}

// renderRoute paints the current route. The search furniture is hidden with a
// body class rather than by setting hidden on each element: those flags belong
// to the search itself (an empty state, a last page), and a route must not be
// able to overwrite them and then guess them back.
// load is false for the very first paint: the view has to switch immediately,
// but its fetch waits for the initial search so the two cannot race for the
// inspector panes. A view that is already on screen is not fetched again:
// closing a card opened over the Stats view lands back on #stats, and
// popstate and hashchange both arrive for that one step.
function renderRoute({ load = true } = {}) {
  if (location.hash.startsWith("#card=")) return;
  const stats = isStatsRoute();
  const wasStats = statsViewOpen();
  document.body.classList.toggle("route-stats", stats);
  $("stats-view").hidden = !stats;
  $("stats-link").textContent = stats ? "Back to search" : "Archive stats";
  if (stats && load && !wasStats) showStats();
}

// leaveStatsRoute returns to the search view with a clean URL — a pushState so
// Back still walks back into the stats page the reader came from.
function leaveStatsRoute() {
  history.pushState(null, "", `${location.pathname}${location.search}`);
  renderRoute();
}

// Dropping every filter, shared by the rail's control and the empty card's.
// Two buttons in two places that have to mean the same thing, and would
// otherwise be two lists of fields for a later filter to be forgotten from.
function clearAllFilters() {
  state.supertype = "";
  state.types = [];
  state.set = "";
  state.rarity = "";
  state.series = "";
  syncFilterControls();
  runSearch({ push: true });
}

// The phone filter panel is a sheet over the page, so the page behind it must
// not scroll while it is open. What was on body.style.overflow before the lock
// is put back rather than blanked, and the lock is only released by whoever
// took it — a second overlay that locks the same way finds the page as it left
// it, and neither one hands the scroll back while the other is still up.
let pageScrollLock = null;

function lockPageScroll() {
  if (pageScrollLock !== null) return;
  pageScrollLock = document.body.style.overflow;
  document.body.style.overflow = "hidden";
}

function releasePageScroll() {
  if (pageScrollLock === null) return;
  document.body.style.overflow = pageScrollLock;
  pageScrollLock = null;
}

// Everything the open sheet covers. The filter rail is not in the list: the
// drawer and the toggle that owns it live there, and they have to stay
// reachable. The skip link is its own entry because it is a direct child of
// body rather than part of any of the three regions, so nothing else can take
// it out of the tab order — and it jumps to a grid the sheet covers; the
// footer is the other direct child of body with a link in it. Between them
// these are every focusable thing outside the rail: the dialog is the
// only other child of body, and it is inert to the page in the other
// direction, by showModal. The strip above the grid lives inside main, so
// main already covers it; it is named anyway, so that moving it out of main
// could not leave its button reachable behind the sheet.
const BEHIND_DRAWER = [".skip-link", ".site-header", "main", ".search-strip", ".observability-rail", ".site-footer"];

// Covering the page is not the same as taking it out of the tab order, so Tab
// past the close button used to land on the search box behind the sheet, with
// the page locked and the caret invisible. inert takes those regions out of
// the tab order and off the accessibility tree for as long as the drawer is
// up. It rides the same open and close paths as the scroll lock, which only
// the phone-width toggle can reach, so desktop never sees it.
function setPageBehindDrawerInert(inert) {
  for (const selector of BEHIND_DRAWER) {
    document.querySelector(selector)?.toggleAttribute("inert", inert);
  }
}

// Closing the drawer: the panel goes away, the page gets its scroll and its
// tab order back, and focus returns to the control that opened it, wherever
// the close came from.
function closeFilterDrawer() {
  $("filter-toggle").setAttribute("aria-expanded", "false");
  releasePageScroll();
  setPageBehindDrawerInert(false);
  $("filter-toggle").scrollIntoView({ block: "nearest" });
  $("filter-toggle").focus({ preventScroll: true });
}

function filterDrawerOpen() {
  return $("filter-toggle").getAttribute("aria-expanded") === "true";
}

// "View query" on the strip above the grid: the rail is below every card at
// these widths, so this takes the visitor to it. The query panel opens first
// (it starts closed on a phone), focus goes to the rail's heading so a
// keyboard or screen-reader visitor arrives there too, and the rail scrolls
// to the top of the page's view, below the header where the header is sticky:
// the page's scroll-padding does that, and its reduced-motion rule is the
// reason the scroll takes no behavior of its own.
function showQueryRail() {
  $("query-inspector").open = true;
  $("telemetry-heading").focus({ preventScroll: true });
  document.querySelector(".observability-rail").scrollIntoView({ block: "start" });
}

// The way back: to the strip, and its button, which is where the visitor
// pressed.
function backToResults() {
  $("search-strip").scrollIntoView({ block: "start" });
  $("view-query").focus({ preventScroll: true });
}

// The strip learns that a search has started or ended from the grid, which is
// where every search, whatever its outcome, raises and lowers its loading
// flag; render.js and api.js are not asked to know about the strip.
function watchSearchLoading() {
  const grid = $("results-grid");
  let loading = null;
  const notify = () => {
    const now = grid.classList.contains("is-loading");
    if (now === loading) return;
    loading = now;
    noteSearchLoading(now);
  };
  new MutationObserver(notify).observe(grid, { attributes: true, attributeFilter: ["class"] });
  notify();
}

// The example searches, on the landing and in the empty state, and the
// spelling correction. Pressing one is the same as typing its query and
// pressing Enter: the box holds it, the filters stay, the search is committed
// (one history entry, or none when the address already holds it), and focus
// then moves to what it found, as after Enter. The button pressed usually
// disappears with the state it sat in, which leaves focus on the body; focus
// the visitor has moved elsewhere by then is left alone.
let queryPresses = 0;

async function runPressedQuery(q, pressed) {
  const press = ++queryPresses;
  closeSuggestions();
  state.q = q;
  state.sort = "";
  state.order = "";
  syncControls();
  const search = commitQuery("enter");
  const outcome = search
    ? await search
    : shownOutcome({ gridHidden: $("results-grid").hidden, emptyHidden: $("empty-state").hidden });
  const active = document.activeElement;
  const target = enterFocusTarget({
    outcome,
    inBox: active === pressed || active === document.body || active === null,
    superseded: press !== queryPresses,
  });
  if (target) document.getElementById(target).focus();
}

function bindExampleEvents() {
  for (const group of [$("landing-examples"), $("empty-examples")]) {
    group.addEventListener("click", (event) => {
      const button = event.target.closest("button[data-q]");
      if (button) runPressedQuery(button.dataset.q, button);
    });
  }
}

function bindCoreEvents() {
  $("search-input").addEventListener("input", (event) => {
    // Typing is a search, and a search belongs on the search view.
    if (statsViewOpen()) leaveStatsRoute();
    state.q = event.target.value;
    noteSearchBoxInput();
    // A new query invalidates an explicit sort: relevance is only meaningful
    // with a query, and the server would flip it anyway.
    state.sort = "";
    state.order = "";
    syncControls();
    scheduleSearch();
    scheduleSuggest();
  });

  $("search-input").addEventListener("keydown", handleSuggestKeydown);
  // Leaving the search box finishes the query, the same as Enter or a pause.
  $("search-input").addEventListener("blur", () => {
    commitQuery("blur");
    // Clicking straight back into the box keeps the list it is about to get.
    setTimeout(() => {
      if (blurCloses({ focused: document.activeElement === $("search-input") })) closeSuggestions();
    }, 100);
  });

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

  // Retrying the search the banner is complaining about. Nothing about the
  // query changed, only whether the archive answered, so this earns no history
  // entry — Back still goes back a query.
  $("retry-search").addEventListener("click", () => {
    runSearch();
  });

  $("search-notice-dismiss").addEventListener("click", () => {
    hideNotice();
    $("search-input").focus({ preventScroll: true });
  });

  $("view-query").addEventListener("click", showQueryRail);
  $("rail-back").addEventListener("click", backToResults);

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
    // Escape belongs to whatever is on top. The suggestion listbox marks its
    // own Escape handled and the card dialog closes itself, so the drawer only
    // takes the key when it is the thing that is open.
    if (event.key === "Escape" && !event.defaultPrevented
      && !$("card-modal").open && filterDrawerOpen()) {
      closeFilterDrawer();
    }
  });

  // Taking a correction is a search the visitor asked for, committed as an
  // Enter would commit it: one history entry, so Back returns to what they
  // actually typed, and the filters kept.
  $("did-you-mean").addEventListener("click", (event) => {
    const button = event.currentTarget;
    if (button.dataset.q) runPressedQuery(button.dataset.q, button);
  });

  // The empty card's two ways out. Both are searches the reader asked for, so
  // both push a history entry — Back returns to the search that found nothing
  // rather than skipping past it.
  $("empty-clear").addEventListener("click", clearAllFilters);

  $("empty-browse").addEventListener("click", () => {
    state.q = "";
    // Leaving the query behind takes its sort with it: relevance means nothing
    // without one, and the archive's own order is what browse is for.
    state.sort = "";
    state.order = "";
    syncControls();
    runSearch({ push: true });
  });

  // The DSL only comes back when the panel is open, so opening it after a
  // search has to fetch once to fill it. Closing fires this too — hence the
  // open check, which also stops the re-fetch looping.
  $("query-inspector").addEventListener("toggle", () => {
    const action = queryPanelRequest($("query-inspector").open, lastResponseHadDSL());
    if (action === "none") return;
    // Whichever view is open owes the inspector its own DSL.
    if (statsViewOpen()) showStats();
    else if (action === "query-only") refreshQuery();
    else runSearch();
  });

  // A comparison is two searches, so the lab only ever asks for one while it
  // is open: here when it is opened, and from the rail itself when a search
  // with a new query lands under an already-open panel. Closing fires this too
  // and the panel declines it, the same shape the inspector above it uses.
  $("ranking-lab").addEventListener("toggle", () => refreshRankingLab());

  // The stats link is a plain anchor on the way in, so it is a real link; on
  // the way back it clears the hash instead of leaving a bare "#" behind.
  $("stats-link").addEventListener("click", (event) => {
    if (!statsViewOpen()) return;
    event.preventDefault();
    leaveStatsRoute();
  });

  // An edited or pasted hash: a card address opens its card, any other one
  // closes the dialog and paints its route.
  window.addEventListener("hashchange", () => {
    syncDialogToAddress();
    renderRoute();
  });

  // Back and Forward restore a previous search. A history entry that only
  // differs by the #card= hash is the modal's, not a search's, and re-running
  // the identical query for it would be pure waste.
  window.addEventListener("popstate", () => {
    // Typing back to the query beneath a draft entry steps back to it; that
    // search is already running, and the entry it lands on has the same hash,
    // so it can neither open nor close the card dialog: it returns before the
    // dialog is consulted.
    if (isOwnStepBack()) return;
    const cancelledSearch = noteHistoryNavigation();
    // Back off a card address closes the dialog, Forward onto one opens it.
    // The dialog's own step back on close arrives here too, finds the dialog
    // already closed, and is an ordinary Back for the typing bookkeeping
    // above: no burst can be open while the dialog covers the search box.
    syncDialogToAddress();
    renderRoute();
    const before = buildParams().toString();
    readStateFromURL();
    syncControls();
    syncFilterControls();
    const stateChanged = buildParams().toString() !== before;
    if (popstateRunsSearch({ ownStepBack: false, stateChanged, cancelledSearch })) {
      runSearch({ fromHistory: true });
    }
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

  $("clear-filters").addEventListener("click", clearAllFilters);

  $("filter-toggle").addEventListener("click", () => {
    const expanded = filterDrawerOpen();
    $("filter-toggle").setAttribute("aria-expanded", String(!expanded));
    if (expanded) {
      releasePageScroll();
      setPageBehindDrawerInert(false);
    } else {
      lockPageScroll();
      setPageBehindDrawerInert(true);
    }
  });

  $("filter-done").addEventListener("click", closeFilterDrawer);

  // Widening past the drawer's breakpoint — a tablet turning landscape — puts
  // the panel back in the rail, where there is nothing to dismiss. The open
  // state goes with it, rather than leaving the page locked and untabbable
  // against a drawer that is no longer on screen.
  window.matchMedia("(max-width: 768px)").addEventListener("change", (event) => {
    if (event.matches || !filterDrawerOpen()) return;
    $("filter-toggle").setAttribute("aria-expanded", "false");
    releasePageScroll();
    setPageBehindDrawerInert(false);
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
  renderExamples();
  renderGuide();
  loadArchiveCounts();
  watchSearchLoading();
  // The strip drops the noun from its count at the width the styles squeeze it
  // for (max-width: 430px in styles.css).
  const narrowScreen = window.matchMedia("(max-width: 430px)");
  setStripCompact(narrowScreen.matches);
  narrowScreen.addEventListener("change", (event) => setStripCompact(event.matches));
  bindCoreEvents();
  bindFilterEvents();
  bindExampleEvents();
  bindModalEvents();
  renderRoute({ load: false });
  refreshHealth();
  runSearch().then(openDeepLink).then(() => {
    if (statsViewOpen()) showStats();
  });
}

init();
