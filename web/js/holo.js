// The holographic card treatment: pointer-tracked 3D tilt with a rarity-gated
// foil pass. Modal only, by design (D6) — 24 simultaneous transform layers on
// a grid full of lazy images is jank bait, and the modal is where the art is
// big enough for the effect to read.
//
// This module owns no markup and looks up no ids: modal.js hands it the
// element. It imports nothing, so it can never take part in a cycle.

// Rarity keywords, checked prismatic-first. These are matched against the
// pinned corpus's 38 real rarities, not invented: "Rare Prism Star" has to
// reach prismatic, "LEGEND", "Double Rare", "Rare BREAK", "Rare Prime" and
// "Classic Collection" are all holographic prints that a bare "holo" test
// would have dropped to a plain sheen.
//
// The keys are deliberately specific. A bare "ace" would foil any future
// rarity that merely contains those three letters, so ACE SPEC and Rare ACE
// are matched by their whole names.
const PRISMATIC = ["hyper", "secret", "rainbow", "crown", "prism", "shiny", "mega"];
const FOIL = [
  "holo", "ultra", "amazing", "radiant", "shining", "legend", "illustration",
  "double rare", "full art", "ace spec", "rare ace", "break", "prime",
  "classic collection", "black white rare",
];

// holoTier grades a rarity into one of three intensities. Everything unknown
// falls to "sheen" — the subtle one — so a corpus that grows a new rarity
// degrades to understated rather than to a rainbow.
export function holoTier(rarity = "") {
  const r = String(rarity).toLowerCase();
  if (PRISMATIC.some((key) => r.includes(key))) return "prismatic";
  if (FOIL.some((key) => r.includes(key))) return "foil";
  return "sheen";
}

const TIER_CLASSES = ["holo-tier-sheen", "holo-tier-foil", "holo-tier-prismatic"];
const POINTER_VARS = ["--px", "--py", "--rx", "--ry"];

// Tilt limit in degrees. Past about 12 the card reads as a falling-over
// rectangle rather than a card being turned to catch the light.
const MAX_TILT = 10;

function clearPointer(art) {
  for (const name of POINTER_VARS) art.style.removeProperty(name);
}

// attachHolo binds the pointer handlers once, at wiring time — not per card.
// Reduced motion is re-read on every event rather than captured at bind time,
// because a viewer can change the OS setting while the page is open.
export function attachHolo(art) {
  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)");

  art.addEventListener("pointermove", (event) => {
    // Touch has no hover, so tracking a finger would mean the card only tilts
    // where a fingertip is already covering it. Touch gets the idle sweep the
    // stylesheet runs instead.
    if (reduced.matches || event.pointerType === "touch") return;
    const rect = art.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    const px = (event.clientX - rect.left) / rect.width;
    const py = (event.clientY - rect.top) / rect.height;
    art.style.setProperty("--px", px.toFixed(3));
    art.style.setProperty("--py", py.toFixed(3));
    art.style.setProperty("--rx", `${((py - 0.5) * -2 * MAX_TILT).toFixed(2)}deg`);
    art.style.setProperty("--ry", `${((px - 0.5) * 2 * MAX_TILT).toFixed(2)}deg`);
  });

  art.addEventListener("pointerleave", () => clearPointer(art));
}

// setHoloTier swaps the intensity class and drops any tilt the previous card
// left behind — a newly opened card must start face-on, not inherit the angle
// the last one was being turned at.
export function setHoloTier(art, tier) {
  art.classList.remove(...TIER_CLASSES);
  art.classList.add(`holo-tier-${tier}`);
  clearPointer(art);
}

// setHoloArtLoaded suppresses the shine when there is no art under it. A foil
// pass over an empty grey rectangle is not a highlight, it is a smudge.
export function setHoloArtLoaded(art, loaded) {
  art.classList.toggle("holo-blank", !loaded);
}
