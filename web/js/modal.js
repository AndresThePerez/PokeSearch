// The card detail dialog: its content, the large-art upgrade, the #card= deep
// link, and focus restoration on close.

import { $, element } from "./util.js";
import { cardsByID, displayType, imageURL } from "./render.js";
import { fetchCardByID } from "./api.js";

let modalOpener = null;
let modalArtCardID = null;

// The grid's small image is already cached, so it shows instantly while the
// large art loads detached; the swap is guarded by card id so a slow earlier
// request can never overwrite a newly clicked card.
function setModalArt(card) {
  const image = $("modal-image");
  modalArtCardID = card.id;
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

function renderModal(card) {
  setModalArt(card);
  $("modal-card-name").textContent = card.name;
  $("modal-set-line").textContent = `${card.set_name} · ${card.number}/${card.set_total} · ${card.release_date}`;

  const meta = [card.rarity, card.hp ? `${card.hp} HP` : "", ...(card.types ?? []).map(displayType), card.artist ? `Art by ${card.artist}` : ""].filter(Boolean);
  $("modal-meta-line").textContent = meta.join(" · ");
  $("modal-meta-line").hidden = meta.length === 0;

  const moveSections = [];
  if (card.attacks?.length) {
    const section = element("section", "modal-section");
    section.append(element("h3", "modal-section-title", "Attacks"));
    for (const attack of card.attacks) section.append(renderAttack(attack));
    moveSections.push(section);
  }
  if (card.abilities?.length) {
    const section = element("section", "modal-section");
    section.append(element("h3", "modal-section-title", "Abilities"));
    for (const ability of card.abilities) section.append(renderAbility(ability));
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

  $("modal-flavor").textContent = card.flavor_text ?? "";
  $("modal-flavor").hidden = !card.flavor_text;
}

function renderAttack(attack) {
  const row = element("article", "move-row");
  const heading = element("div", "move-heading");
  const name = element("h4", "", attack.name);
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
  if (attack.text) row.append(element("p", "", attack.text));
  return row;
}

function renderAbility(ability) {
  const row = element("article", "move-row ability-row");
  const heading = element("div", "move-heading");
  heading.append(element("h4", "", ability.name), element("span", "ability-kind", ability.type));
  row.append(heading);
  if (ability.text) row.append(element("p", "", ability.text));
  return row;
}

export function openModal(card, { updateHash = true, opener = null } = {}) {
  modalOpener = opener;
  renderModal(card);
  if (updateHash) location.hash = `card=${encodeURIComponent(card.id)}`;
  if (!$("card-modal").open) $("card-modal").showModal();
}

export function closeModal() {
  if ($("card-modal").open) $("card-modal").close();
}

function clearCardHash() {
  if (location.hash.startsWith("#card=")) {
    history.replaceState(null, "", `${location.pathname}${location.search}`);
  }
}

export async function openDeepLink() {
  if (!location.hash.startsWith("#card=")) return;
  const id = decodeURIComponent(location.hash.slice("#card=".length));
  if (!id) return;
  let card = cardsByID.get(id);
  if (!card) {
    try {
      card = await fetchCardByID(id);
      if (card) cardsByID.set(card.id, card);
    } catch {
      return;
    }
  }
  if (card) openModal(card, { updateHash: false });
}

export function bindModalEvents() {
  $("results-grid").addEventListener("click", (event) => {
    const button = event.target.closest(".card-open");
    if (!button) return;
    const card = cardsByID.get(button.dataset.id);
    if (card) openModal(card, { opener: button });
  });

  $("modal-close").addEventListener("click", closeModal);
  $("card-modal").addEventListener("click", (event) => {
    if (event.target === $("card-modal")) closeModal();
  });
  $("card-modal").addEventListener("close", () => {
    clearCardHash();
    const opener = modalOpener;
    modalOpener = null;
    opener?.focus();
  });
}
