// The card detail dialog: its content, the large-art upgrade, the #card= deep
// link and its history entry, and focus restoration on close.

import { $, element } from "./util.js";
import { cardsByID, displayType, rarityLabel, imageURL, branchLabel, markWithin } from "./render.js";
import { fetchExplain, fetchLinkedCard, showNotice } from "./api.js";
import { queryText } from "./state.js";
import { holoTier, attachHolo, setHoloTier, setHoloArtLoaded } from "./holo.js";

let modalOpener = null;
let modalArtCardID = null;
let explainCardID = null;
// Whether #modal-explain currently holds a real breakdown. The apology a
// failed fetch leaves behind is a child of the panel too, so the panel's own
// contents cannot tell a cached answer from an error still worth retrying.
let explainRendered = false;
// Whether a breakdown request is in flight, and whether the last one failed:
// together with the panel's visibility they are the button's whole state.
let explainLoading = false;
let explainFailed = false;
// The card the dialog shows, or null while it is closed. Set before the
// dialog opens and cleared when it closes, so an address change that arrives
// in between is judged against the card actually on its way to the screen.
let openCardID = null;
// The hash the page last acted on. popstate and hashchange both fire for one
// fragment navigation; comparing against this makes the second one a no-op.
let addressHash = "";
// The marker this page puts on a #card= history entry it pushed. Its presence
// is what makes closing the dialog a step back rather than an in-place edit:
// it survives Back, Forward and a reload, and a pasted link never carries it.
const CARD_ENTRY_STATE = { cardOverlay: true };
// The hash the modal replaced when it opened. The modal is an overlay on a
// route, not a route of its own, so closing it has to put the reader back on
// the page they were on — #stats included.
let hashBeforeModal = "";
// What the body's inline overflow was before the dialog locked it, put back on
// close rather than blanked. The phone filter drawer locks the page the same
// way, and in practice the two never overlap: the open drawer marks the
// regions behind it inert — the results grid among them — so no card button
// can be reached by pointer or by Tab, and in the other direction showModal
// inerts the drawer's own toggle. They still do not have to trust each other.
// The drawer can release its lock without anyone clicking anything — widening
// past its breakpoint does exactly that — so the restore below is conditional:
// it only puts this value back while the lock it took is still the one on the
// page, and never writes a stale "hidden" over a page someone else freed.
let overflowBeforeModal = "";

// The grid's small image is already cached, so it shows instantly while the
// large art loads detached; the swap is guarded by card id so a slow earlier
// request can never overwrite a newly clicked card.
function setModalArt(card) {
  const image = $("modal-image");
  modalArtCardID = card.id;
  // The rarity drives the foil intensity: this is the one place the index's
  // rarity field stops being a filter and becomes presentation.
  setHoloTier($("modal-art"), holoTier(card.rarity));
  setHoloArtLoaded($("modal-art"), true);
  image.src = imageURL(card, "small");
  image.alt = card.name;
  image.classList.add("is-upgrading");
  image.classList.remove("art-failed");
  const largeURL = imageURL(card, "large");
  const loader = new Image();
  loader.addEventListener("load", () => {
    if (modalArtCardID !== card.id) return;
    image.src = largeURL;
    image.classList.remove("is-upgrading");
  }, { once: true });
  loader.addEventListener("error", () => {
    if (modalArtCardID !== card.id) return;
    image.classList.remove("is-upgrading");
    image.classList.add("art-failed");
  }, { once: true });
  loader.src = largeURL;
}

// explainButtonView decides how the breakdown button reads in each state:
// "idle" (nothing shown), "open" (a breakdown shown), "loading" (a request in
// flight) and "failed" (the apology shown, and pressing the button retries).
// expanded follows the panel's own visibility; busy disables the button.
export function explainButtonView(status) {
  switch (status) {
    case "open":
      return { label: "Hide the breakdown", expanded: true, busy: false };
    case "loading":
      return { label: "Explaining…", expanded: false, busy: true };
    case "failed":
      return { label: "Try again", expanded: true, busy: false };
    default:
      return { label: "Why this card?", expanded: false, busy: false };
  }
}

// syncExplainToggle points the button's label and aria-expanded at the panel's
// own visibility, so no path can leave the control asserting a state the panel
// is not in. Every route through the disclosure ends here.
function syncExplainToggle() {
  let status = "idle";
  if (explainLoading) status = "loading";
  else if (explainFailed) status = "failed";
  else if (!$("modal-explain").hidden) status = "open";
  const view = explainButtonView(status);
  $("modal-why").textContent = view.label;
  $("modal-why").setAttribute("aria-expanded", view.expanded ? "true" : "false");
  $("modal-why").disabled = view.busy;
}

// resetExplain returns the score-anatomy panel to its closed state. Opening a
// different card must not leave the previous card's breakdown on screen, and
// without a query there is nothing to explain at all.
function resetExplain(card) {
  explainCardID = card.id;
  explainRendered = false;
  explainLoading = false;
  explainFailed = false;
  $("modal-explain").replaceChildren();
  $("modal-explain").hidden = true;
  $("modal-why").hidden = !queryText();
  syncExplainToggle();
}

// renderExplain draws the per-branch score bars: how much each relevance
// clause contributed to this card's score, longest bar first by value. The
// bars are proportional to the strongest branch, so the shape of the answer —
// "the exact name match is doing all the work" — reads at a glance.
function renderExplain(data) {
  const panel = $("modal-explain");
  const max = Math.max(0, ...data.branches.map((b) => b.score));
  const matched = data.branches.filter((b) => b.matched).length;

  const rows = [element("p", "explain-head", data.found
    ? `Score ${data.score} · ${matched} of ${data.branches.length} branches matched “${data.q}”`
    : "This card is not in the index, so no branch could match it.")];

  for (const branch of data.branches) {
    const row = element("div", `explain-row${branch.matched ? "" : " is-unmatched"}`);
    row.append(element("span", "explain-name", branchLabel(branch.name)));
    const track = element("span", "explain-track");
    const fill = element("span", `explain-fill match-${branch.name}`);
    // An unmatched branch keeps a visible sliver rather than nothing: "scored
    // zero" and "was not considered" must not look the same.
    fill.style.width = branch.matched && max > 0 ? `${(branch.score / max) * 100}%` : "2px";
    track.append(fill);
    row.append(track, element("span", "explain-score", branch.score.toFixed(1)));
    panelRow(row, branch);
    rows.push(row);
  }

  const raw = element("details", "explain-raw");
  raw.append(element("summary", "", "Raw explain response"));
  raw.append(element("pre", "", JSON.stringify(data, null, 2)));
  rows.push(raw);

  panel.replaceChildren(...rows);
  panel.hidden = false;
}

// panelRow labels the row for assistive tech, where a bar width says nothing.
function panelRow(row, branch) {
  row.setAttribute("role", "group");
  row.setAttribute("aria-label", branch.matched
    ? `${branchLabel(branch.name)} branch scored ${branch.score.toFixed(1)}`
    : `${branchLabel(branch.name)} branch did not match`);
}

// toggleExplain is the disclosure's only entry point. Only a rendered
// breakdown is a disclosure at all: it cannot go stale while the card stays
// open — resetExplain drops it the moment another card does — so it collapses
// and reopens from what is already on screen, with no second request. Until
// there is one, the click fetches, which is what makes a failed attempt
// retryable: its apology is written into the panel but is not an answer.
// The button itself never moves or goes away, so the keyboard stays exactly
// where the reader left it.
function toggleExplain() {
  if (!explainRendered) {
    loadExplain();
    return;
  }
  $("modal-explain").hidden = !$("modal-explain").hidden;
  syncExplainToggle();
}

async function loadExplain() {
  const id = explainCardID;
  const q = queryText();
  if (!id || !q) return;
  // A retry takes the previous apology down while it runs, so the panel and
  // the button never disagree about what is on screen.
  explainLoading = true;
  explainFailed = false;
  $("modal-explain").hidden = true;
  syncExplainToggle();
  try {
    const data = await fetchExplain(id, q);
    // The user can click through to another card while this is in flight; a
    // late answer must not describe a card that is no longer open.
    if (explainCardID !== id) return;
    renderExplain(data);
    explainRendered = true;
  } catch {
    if (explainCardID !== id) return;
    $("modal-explain").replaceChildren(
      element("p", "explain-head", "The score breakdown is unavailable right now."));
    $("modal-explain").hidden = false;
    explainFailed = true;
  } finally {
    // Whatever the panel ended up showing — a breakdown, an apology, or
    // nothing, when a late answer was discarded — the button is re-enabled and
    // relabelled to match it. resetExplain owns the discarded case.
    if (explainCardID === id) {
      explainLoading = false;
      syncExplainToggle();
    }
  }
}

function renderModal(card, highlight) {
  setModalArt(card);
  resetExplain(card);
  $("modal-card-name").textContent = card.name;
  $("modal-set-line").textContent = `${card.set_name} · ${card.number}/${card.set_total} · ${card.release_date}`;

  const meta = [rarityLabel(card.rarity), card.hp ? `${card.hp} HP` : "", ...(card.types ?? []).map(displayType), card.artist ? `Art by ${card.artist}` : ""].filter(Boolean);
  $("modal-meta-line").textContent = meta.join(" · ");
  $("modal-meta-line").hidden = meta.length === 0;

  const moveSections = [];
  if (card.attacks?.length) {
    const section = element("section", "modal-section");
    section.append(element("h3", "modal-section-title", "Attacks"));
    for (const attack of card.attacks) section.append(renderAttack(attack, highlight));
    moveSections.push(section);
  }
  if (card.abilities?.length) {
    const section = element("section", "modal-section");
    section.append(element("h3", "modal-section-title", "Abilities"));
    for (const ability of card.abilities) section.append(renderAbility(ability, highlight));
    moveSections.push(section);
  }
  $("modal-attacks").replaceChildren(...moveSections);
  $("modal-attacks").hidden = moveSections.length === 0;

  const battle = [];
  if (card.weaknesses?.length) battle.push(`Weakness ${card.weaknesses.map((value) => `${displayType(value.type)} ${value.value}`).join(", ")}`);
  if (card.resistances?.length) battle.push(`Resistance ${card.resistances.map((value) => `${displayType(value.type)} ${value.value}`).join(", ")}`);
  if (card.retreat_cost) battle.push(`Retreat ${card.retreat_cost}`);
  $("modal-battle-line").replaceChildren(...battle.map((value) => element("span", "battle-chip", value)));
  $("modal-battle-line").hidden = battle.length === 0;

  const flavor = markWithin(card.flavor_text, highlight?.["flavor_text"]);
  if (flavor) $("modal-flavor").replaceChildren(...flavor);
  else $("modal-flavor").textContent = card.flavor_text ?? "";
  $("modal-flavor").hidden = !card.flavor_text;
}

// The trading card game's own one-letter energy codes. The first letter of
// the name is not enough: Fire and Fighting would share F, Darkness and Dragon
// D, and the colour behind the letter is no help to everyone.
const ENERGY_GLYPHS = {
  Grass: "G",
  Fire: "R",
  Water: "W",
  Lightning: "L",
  Psychic: "P",
  Fighting: "F",
  Darkness: "D",
  Metal: "M",
  Fairy: "Y",
  Dragon: "N",
  Colorless: "C",
};

// energyIcon decides how one energy in an attack cost is drawn: its glyph,
// its accessible name (with the type name the page shows elsewhere, so Steel
// and Normal rather than Metal and Colorless) and its class. A type the map
// does not know still gets a labelled icon, with a neutral colour.
export function energyIcon(type) {
  const known = typeof type === "string" && Object.hasOwn(ENERGY_GLYPHS, type);
  const shown = typeof type === "string" ? displayType(type).trim() : "";
  return {
    glyph: known ? ENERGY_GLYPHS[type] : "?",
    name: `${shown || "Unknown"} energy`,
    className: known ? `energy-icon energy-${type.toLowerCase()}` : "energy-icon",
  };
}

function renderAttack(attack, highlight) {
  const row = element("article", "move-row");
  const heading = element("div", "move-heading");
  const name = element("h4", "", attack.name);
  setMarked(name, attack.name, highlight?.["attacks.name"]);
  const damage = element("strong", "move-damage", attack.damage || "—");
  heading.append(name, damage);

  const costs = element("div", "energy-cost");
  for (const type of attack.cost ?? []) {
    const { glyph, name, className } = energyIcon(type);
    const icon = element("span", className, glyph);
    icon.title = name;
    icon.setAttribute("role", "img");
    icon.setAttribute("aria-label", name);
    costs.append(icon);
  }
  row.append(heading);
  if (costs.childElementCount) row.append(costs);
  if (attack.text) {
    const text = element("p", "", attack.text);
    setMarked(text, attack.text, highlight?.["attacks.text"]);
    row.append(text);
  }
  return row;
}

// setMarked swaps an element's plain text for the highlighted rendering of the
// same string, when a fragment for it exists. Absent highlights — a deep link
// opened with no search behind it — leave the plain text exactly as it was.
function setMarked(node, original, fragments) {
  const marked = markWithin(original, fragments);
  if (marked) node.replaceChildren(...marked);
}

function renderAbility(ability, highlight) {
  const row = element("article", "move-row ability-row");
  const heading = element("div", "move-heading");
  const name = element("h4", "", ability.name);
  setMarked(name, ability.name, highlight?.["abilities.name"]);
  heading.append(name, element("span", "ability-kind", ability.type));
  row.append(heading);
  if (ability.text) {
    const text = element("p", "", ability.text);
    setMarked(text, ability.text, highlight?.["abilities.text"]);
    row.append(text);
  }
  return row;
}

// cardHashID reads the card id out of a #card= hash: null when the hash is
// not a card address at all, the id as written when it cannot be decoded.
export function cardHashID(hash) {
  if (typeof hash !== "string" || !hash.startsWith("#card=")) return null;
  const raw = hash.slice("#card=".length);
  try {
    return decodeURIComponent(raw);
  } catch {
    return raw;
  }
}

// cardRouteAction decides what a change of address means for the dialog,
// given the hash before and after and the id of the card the dialog shows
// (null when it is closed): open a card, close the dialog, or nothing.
export function cardRouteAction({ from, to, openCardID: shown }) {
  if (from === to) return { action: "nothing" };
  const id = cardHashID(to);
  if (id !== null) return id === shown ? { action: "nothing" } : { action: "open", id };
  return shown !== null && shown !== undefined ? { action: "close" } : { action: "nothing" };
}

// cardExitStep decides how closing the dialog leaves its card address: one
// step back when this page pushed the entry, so the visitor's Back does not
// land on a card they already closed; an in-place edit when they arrived on
// the card from a link, so closing it never takes them off the site; and
// nothing when the address has already moved on — the close came from Back.
export function cardExitStep({ addressIsCard, pushedByPage }) {
  if (!addressIsCard) return "none";
  return pushedByPage ? "back" : "replace";
}

// The shape of a card id the API accepts (search.CardIDPattern on the server).
const CARD_ID = /^[A-Za-z0-9][A-Za-z0-9._!?-]{0,127}$/;

const BAD_CARD_LINK = "That link does not point to a PokéSearch card.";
const CARD_NOT_FOUND = "That card isn't in the archive.";
const CARD_UNAVAILABLE = "PokéSearch could not load that card. Reload the page to try again.";

// deepLinkFailure decides how a #card= link that opened nothing is reported:
// the message for the notice, and whether the hash is cleared. A malformed or
// unknown id will never open, so its hash goes. A network failure, a 5xx or a
// rate limit says nothing about the card, so the hash stays for a reload to
// try again. A card that was found is no failure: null.
export function deepLinkFailure({ id, status = 0, found = false, networkError = false }) {
  if (typeof id !== "string" || !CARD_ID.test(id)) return { message: BAD_CARD_LINK, clearHash: true };
  if (networkError || status >= 500 || status === 408 || status === 429) {
    return { message: CARD_UNAVAILABLE, clearHash: false };
  }
  if (status >= 400 && status !== 404) return { message: BAD_CARD_LINK, clearHash: true };
  if (status === 404 || !found) return { message: CARD_NOT_FOUND, clearHash: true };
  return null;
}

// linkFailureStep decides what a card link that opened nothing does to the
// address, given deepLinkFailure's verdict, the card the dialog shows (null
// when it is closed), whether the current entry carries the page's marker and
// the hash the dialog returns to. With a dialog open, the address goes back
// to the card on screen and the entry keeps its marker: the visitor keeps the
// card they had, and the address never names a card that is not the one
// shown. With no dialog, a hash that will never open is cleared, and one that
// could not load is kept for a reload. The dialog is never closed for it.
export function linkFailureStep({ failure, openCardID: shown, marked, before }) {
  if (shown !== null && shown !== undefined) {
    return { closeDialog: false, write: "replace", hash: `#card=${encodeURIComponent(shown)}`, marked: marked === true };
  }
  if (failure?.clearHash) return { closeDialog: false, write: "replace", hash: before, marked: false };
  return { closeDialog: false, write: "none", hash: null, marked: false };
}

// cardOpenStep decides how opening a card from the grid writes history, given
// the current hash, the card's own hash, whether the current entry carries
// the page's marker and the hash the dialog returns to. From an ordinary
// address it pushes a marked entry and remembers that address. Over a card
// address — a link kept because it could not load — it replaces that entry,
// keeping its marker and the address it returns to, so no card address is
// left beneath the new one for the close to step back onto.
export function cardOpenStep({ hash, target, marked, before }) {
  if (hash === target) return { write: "none", marked: marked === true, before };
  if (cardHashID(hash) === null) return { write: "push", marked: true, before: hash };
  return { write: "replace", marked: marked === true, before };
}

function addressWithout(hash) {
  return `${location.pathname}${location.search}${hash}`;
}

export function openModal(card, { updateHash = true, opener = null, highlight = null } = {}) {
  modalOpener = opener;
  openCardID = card.id;
  renderModal(card, highlight);
  if (updateHash) {
    const target = `#card=${encodeURIComponent(card.id)}`;
    const step = cardOpenStep({ hash: location.hash, target, marked: entryMarked(), before: hashBeforeModal });
    hashBeforeModal = step.before;
    // pushState and replaceState rather than location.hash: a fragment
    // navigation would fire popstate and hashchange for a change the page
    // made itself.
    const entryState = step.marked ? CARD_ENTRY_STATE : null;
    if (step.write === "push") history.pushState(entryState, "", addressWithout(target));
    else if (step.write === "replace") history.replaceState(entryState, "", addressWithout(target));
    addressHash = location.hash;
  }
  if (!$("card-modal").open) {
    $("card-modal").showModal();
    // showModal makes the page behind the dialog inert, not unscrollable: a
    // swipe near the edge of a phone still moves the results underneath, and a
    // desktop shows two scrollbars. The lock is taken only on a real open, so a
    // second call while the dialog is up cannot overwrite the saved value.
    overflowBeforeModal = document.body.style.overflow;
    document.body.style.overflow = "hidden";
  }
}

export function closeModal() {
  if ($("card-modal").open) $("card-modal").close();
}

// entryMarked says whether the current history entry is a card entry this
// page pushed.
function entryMarked() {
  return history.state?.cardOverlay === true;
}

// leaveCardAddress carries out cardExitStep when the dialog closes.
function leaveCardAddress() {
  const step = cardExitStep({
    addressIsCard: cardHashID(location.hash) !== null,
    pushedByPage: entryMarked(),
  });
  if (step === "back") {
    // The popstate this causes finds the dialog already closed, so it opens
    // and closes nothing.
    history.back();
  } else if (step === "replace") {
    history.replaceState(null, "", addressWithout(hashBeforeModal));
    addressHash = location.hash;
  }
}

// openLinkedCard opens the card a #card= address names, from the grid when it
// is already there, from the API otherwise. A link that opens nothing says so
// in the notice. A visitor who moved on while it loaded is left where they are.
async function openLinkedCard(id) {
  let entry = cardsByID.get(id);
  if (!entry) {
    let failure = deepLinkFailure({ id });
    if (CARD_ID.test(id)) {
      const { status, card, networkError } = await fetchLinkedCard(id);
      failure = deepLinkFailure({ id, status, found: card !== null, networkError });
      // A deep-linked card was fetched by id, with no query behind it, so it
      // has no highlights — the modal falls back to plain text.
      if (card) {
        entry = { card, highlight: null };
        cardsByID.set(card.id, entry);
      }
    }
    if (cardHashID(location.hash) !== id) return;
    if (failure) {
      showNotice(failure.message);
      const step = linkFailureStep({ failure, openCardID, marked: entryMarked(), before: hashBeforeModal });
      if (step.closeDialog) closeModal();
      if (step.write === "replace") {
        history.replaceState(step.marked ? CARD_ENTRY_STATE : null, "", addressWithout(step.hash));
        addressHash = location.hash;
      }
      return;
    }
  }
  if (cardHashID(location.hash) !== id) return;
  openModal(entry.card, { updateHash: false, highlight: entry.highlight });
}

// openDeepLink opens the card the page was loaded on.
export async function openDeepLink() {
  addressHash = location.hash;
  const id = cardHashID(location.hash);
  if (id === null) return;
  hashBeforeModal = "";
  await openLinkedCard(id);
}

// syncDialogToAddress makes the dialog follow the address after Back,
// Forward or an edited hash: a card address opens its card, any other address
// closes the dialog. Both popstate and hashchange call it; the second call
// for one navigation finds nothing changed.
export function syncDialogToAddress() {
  const from = addressHash;
  const to = location.hash;
  addressHash = to;
  const step = cardRouteAction({ from, to, openCardID });
  if (step.action === "close") {
    closeModal();
  } else if (step.action === "open") {
    if (cardHashID(from) === null) hashBeforeModal = from;
    openLinkedCard(step.id);
  }
}

export function bindModalEvents() {
  // Bound once, not per card: the handlers read the element's rect live, so
  // they do not care which card is currently in it.
  attachHolo($("modal-art"));
  // Both card sizes can 404 — the art is third-party. A foil pass over an
  // empty rectangle is a smudge, so the shine goes with the art, and comes
  // back with it: the small image can fail while the large one still loads.
  $("modal-image").addEventListener("error", () => setHoloArtLoaded($("modal-art"), false));
  $("modal-image").addEventListener("load", () => setHoloArtLoaded($("modal-art"), true));
  addressHash = location.hash;

  $("results-grid").addEventListener("click", (event) => {
    const button = event.target.closest(".card-open");
    if (!button) return;
    const entry = cardsByID.get(button.dataset.id);
    if (entry) openModal(entry.card, { opener: button, highlight: entry.highlight });
  });

  $("modal-why").addEventListener("click", toggleExplain);
  $("modal-close").addEventListener("click", closeModal);
  $("card-modal").addEventListener("click", (event) => {
    if (event.target === $("card-modal")) closeModal();
  });
  $("card-modal").addEventListener("close", () => {
    // The dialog's own close event, so Escape, the backdrop and the close
    // button all release the lock by the same path — and the page can scroll
    // again before focus goes back to the card that opened it. Only a lock
    // that is still held is released: if something else freed the page while
    // the dialog was up, the value saved on open is stale and putting it back
    // would relock a page nobody is covering.
    if (document.body.style.overflow === "hidden") {
      document.body.style.overflow = overflowBeforeModal;
    }
    openCardID = null;
    leaveCardAddress();
    const opener = modalOpener;
    modalOpener = null;
    opener?.focus();
  });
}

