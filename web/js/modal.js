// The card detail dialog: its content, the large-art upgrade, the #card= deep
// link, and focus restoration on close.

import { $, element } from "./util.js";
import { cardsByID, displayType, imageURL, branchLabel, markWithin } from "./render.js";
import { fetchCardByID, fetchExplain } from "./api.js";
import { queryText } from "./state.js";
import { holoTier, attachHolo, setHoloTier, setHoloArtLoaded } from "./holo.js";

let modalOpener = null;
let modalArtCardID = null;
let explainCardID = null;
// Whether #modal-explain currently holds a real breakdown. The apology a
// failed fetch leaves behind is a child of the panel too, so the panel's own
// contents cannot tell a cached answer from an error still worth retrying.
let explainRendered = false;
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

// syncExplainToggle points the button's label and aria-expanded at the panel's
// own visibility, so no path can leave the control asserting a state the panel
// is not in. Every route through the disclosure ends here.
function syncExplainToggle() {
  const shown = !$("modal-explain").hidden;
  $("modal-why").textContent = shown ? "Hide the breakdown" : "Why this card?";
  $("modal-why").setAttribute("aria-expanded", shown ? "true" : "false");
}

// resetExplain returns the score-anatomy panel to its closed state. Opening a
// different card must not leave the previous card's breakdown on screen, and
// without a query there is nothing to explain at all.
function resetExplain(card) {
  explainCardID = card.id;
  explainRendered = false;
  $("modal-explain").replaceChildren();
  $("modal-explain").hidden = true;
  $("modal-why").hidden = !queryText();
  $("modal-why").disabled = false;
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
  $("modal-why").disabled = true;
  $("modal-why").textContent = "Explaining…";
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
  } finally {
    // Whatever the panel ended up showing — a breakdown, an apology, or
    // nothing, when a late answer was discarded — the button is re-enabled and
    // relabelled to match it. resetExplain owns the discarded case.
    if (explainCardID === id) {
      $("modal-why").disabled = false;
      syncExplainToggle();
    }
  }
}

function renderModal(card, highlight) {
  setModalArt(card);
  resetExplain(card);
  $("modal-card-name").textContent = card.name;
  $("modal-set-line").textContent = `${card.set_name} · ${card.number}/${card.set_total} · ${card.release_date}`;

  const meta = [card.rarity, card.hp ? `${card.hp} HP` : "", ...(card.types ?? []).map(displayType), card.artist ? `Art by ${card.artist}` : ""].filter(Boolean);
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

function renderAttack(attack, highlight) {
  const row = element("article", "move-row");
  const heading = element("div", "move-heading");
  const name = element("h4", "", attack.name);
  setMarked(name, attack.name, highlight?.["attacks.name"]);
  const damage = element("strong", "move-damage", attack.damage || "—");
  heading.append(name, damage);

  const costs = element("div", "energy-cost");
  for (const type of attack.cost ?? []) {
    const label = displayType(type);
    const icon = element("span", `energy-icon energy-${type.toLowerCase()}`, label[0]);
    icon.title = label;
    icon.setAttribute("aria-label", label);
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

export function openModal(card, { updateHash = true, opener = null, highlight = null } = {}) {
  modalOpener = opener;
  renderModal(card, highlight);
  if (updateHash) {
    if (!location.hash.startsWith("#card=")) hashBeforeModal = location.hash;
    location.hash = `card=${encodeURIComponent(card.id)}`;
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

function clearCardHash() {
  if (location.hash.startsWith("#card=")) {
    history.replaceState(null, "", `${location.pathname}${location.search}${hashBeforeModal}`);
  }
}

export async function openDeepLink() {
  if (!location.hash.startsWith("#card=")) return;
  const id = decodeURIComponent(location.hash.slice("#card=".length));
  if (!id) return;
  let entry = cardsByID.get(id);
  if (!entry) {
    try {
      const card = await fetchCardByID(id);
      // A deep-linked card was fetched by id, with no query behind it, so it
      // has no highlights — the modal falls back to plain text.
      if (card) entry = { card, highlight: null };
      if (entry) cardsByID.set(card.id, entry);
    } catch {
      return;
    }
  }
  if (entry) openModal(entry.card, { updateHash: false, highlight: entry.highlight });
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
    clearCardHash();
    const opener = modalOpener;
    modalOpener = null;
    opener?.focus();
  });
}
