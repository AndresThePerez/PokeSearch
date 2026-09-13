// The Stats view (#stats): the whole archive described in four hand-rolled
// charts, from one cached /api/stats response.
//
// No chart library, by design (D7) — every mark here is a CSS box or an SVG
// node built with textContent, the same discipline the rest of the frontend
// keeps. The visual grammar follows one rule: bar LENGTH carries magnitude.
// Colour is the accent everywhere it means nothing, and the type's own
// canonical hue on the energy rows, where it means identity — the bar and the
// dot beside it take that hue from the same --type-color the filter rail uses,
// so the two can never disagree. Nothing here is readable by colour alone.
//
// Every value on this page is also readable as text: each bar row prints its
// own count, and the response inspector beside the charts holds the raw
// numbers. The hover tooltips are an enhancement, never the only way to read a
// value.

import { $, element } from "./util.js";
import { displayType } from "./render.js";
import { fetchStats, fetchTopCard } from "./api.js";
import { renderInspector, debugEnabled } from "./telemetry.js";

// The response is cached for the page's lifetime, the same way the server
// caches it for the process's: the corpus cannot change under a running page.
// cachedHasDSL tracks whether the cached copy came back with debug=1, so
// opening the query inspector can refetch exactly once for the DSL.
let cached = null;
let cachedHasDSL = false;
let inFlight = null;

// How many rarity rows the archive breakdown shows before folding the tail
// into a single line. Thirty-eight bars is a list, not a chart.
const RARITY_ROWS = 10;

// The HP histogram's band width, matching search.hpBucketWidth on the server.
const HP_BAND = 30;

export async function showStats() {
  const needsFetch = !cached || (debugEnabled() && !cachedHasDSL);
  if (!needsFetch) {
    renderInspector(cached.dsl, cached);
    return;
  }
  if (inFlight) return inFlight;
  renderMessage("Measuring the archive…");
  const wantDSL = debugEnabled();
  inFlight = fetchStats(wantDSL)
    .then((data) => {
      cached = data;
      cachedHasDSL = wantDSL;
      renderInspector(data.dsl, data);
      renderStatsView(data);
      return renderSuperlatives(data);
    })
    .catch(() => {
      renderMessage("The archive statistics are unavailable right now.");
    })
    .finally(() => {
      inFlight = null;
    });
  return inFlight;
}

function renderMessage(text) {
  $("stats-charts").replaceChildren(element("p", "stats-message", text));
}

function renderStatsView(data) {
  $("stats-total").textContent = `${Number(data.total).toLocaleString()} cards in aggregate`;
  $("stats-charts").replaceChildren(
    yearChart(data.per_year),
    hpChart(data.hp, data.max_hp),
    typeChart(data.types),
    classAndRarityChart(data.supertype, data.rarity),
  );
  // Sanity checks for the three breakdowns that must account for every card.
  // types, rarity and hp deliberately do not: a Trainer has no type and no HP,
  // and a handful of cards carry no rarity at all.
  console.assert(sum(data.per_year) === data.total, "per_year must sum to total");
  console.assert(sum(data.supertype) === data.total, "supertype must sum to total");
  console.assert(sum(data.series) === data.total, "series must sum to total");
}

function sum(buckets) {
  return buckets.reduce((total, bucket) => total + bucket.count, 0);
}

function maxCount(buckets) {
  return Math.max(...buckets.map((bucket) => bucket.count), 1);
}

function chartBlock(title, note, body, { wide = false } = {}) {
  const block = element("section", `chart-block${wide ? " is-wide" : ""}`);
  block.append(element("h3", "chart-title", title));
  if (note) block.append(element("p", "chart-note", note));
  block.append(body);
  return block;
}

// barRow is the shared mark: a label, a track, a fill proportional to the
// largest value in its own chart, and the count as text at the end. The count
// is the direct label — no tooltip is required to read this chart.
function barRow(label, count, max, { dotClass = "" } = {}) {
  const row = element("div", "chart-row");
  const name = element("span", "chart-label");
  if (dotClass) {
    const dot = element("span", `type-dot ${dotClass}`);
    dot.setAttribute("aria-hidden", "true");
    name.append(dot);
  }
  name.append(element("span", "chart-label-text", label));
  const track = element("span", "chart-track");
  const fill = element("span", dotClass ? `chart-fill is-typed ${dotClass}` : "chart-fill");
  fill.style.width = `${max ? ((count / max) * 100).toFixed(1) : 0}%`;
  track.append(fill);
  row.append(name, track, element("span", "chart-value", Number(count).toLocaleString()));
  return row;
}

function barRows(buckets, label = (bucket) => bucket.value, options = () => ({})) {
  const max = maxCount(buckets);
  const rows = element("div", "chart-rows");
  for (const bucket of buckets) {
    rows.append(barRow(label(bucket), bucket.count, max, options(bucket)));
  }
  return rows;
}

// The release timeline is the one chart with too many marks to label each one,
// so it is a column chart: every fifth year is labelled, the busiest year is
// emphasised in full accent and named in the caption, and each column carries
// its exact count as a tooltip.
function yearChart(perYear) {
  if (!perYear.length) return chartBlock("Cards printed per year", "", element("p", "chart-note", "No releases recorded."));
  const max = maxCount(perYear);
  const peak = perYear.reduce((best, bucket) => (bucket.count > best.count ? bucket : best), perYear[0]);
  const first = perYear[0];
  const last = perYear[perYear.length - 1];

  const chart = element("div", "chart-cols");
  chart.setAttribute("role", "img");
  chart.setAttribute("aria-label",
    `Cards printed per year from ${first.year} to ${last.year}. Busiest year ${peak.year} with ${Number(peak.count).toLocaleString()} cards.`);
  for (const bucket of perYear) {
    const column = element("span", `chart-col${bucket === peak ? " is-peak" : ""}`);
    column.title = `${bucket.year} — ${Number(bucket.count).toLocaleString()} cards`;
    const fill = element("span", "chart-col-fill");
    fill.style.height = `${((bucket.count / max) * 100).toFixed(1)}%`;
    const decade = Number(bucket.year) % 10 === 0;
    const label = element("span",
      `chart-col-label${decade ? " is-decade" : ""}`,
      Number(bucket.year) % 5 === 0 || bucket === last ? bucket.year : "");
    column.append(fill, label);
    chart.append(column);
  }
  return chartBlock(
    "Cards printed per year",
    `${first.year} to ${last.year}. The print rate explodes after 2010; the busiest year is ${peak.year}, with ${Number(peak.count).toLocaleString()} cards.`,
    chart, { wide: true });
}

// HP bands, 30 points wide, exactly as the server's histogram cut them.
function hpChart(hp, maxHP) {
  const last = hp.length - 1;
  const label = (bucket, index) => (index === last ? `${bucket.from}+` : `${bucket.from}–${bucket.from + HP_BAND - 1}`);
  const buckets = hp.map((bucket, index) => ({ value: label(bucket, index), count: bucket.count }));
  return chartBlock(
    "HP distribution",
    `Power creep in one chart: the archive tops out at ${maxHP} HP, a number no card in the first decade came close to. Only Pokémon carry HP, so Trainers and Energy are absent here.`,
    barRows(buckets));
}

// The energy types. Colour is identity here and it comes from the type's own
// canonical dot — the same one the filter rail shows — and the bar now carries
// that same hue, so the mark encoding the number is the mark carrying the
// identity. Length still carries the magnitude; the count is printed either way.
function typeChart(types) {
  return chartBlock(
    "Energy types",
    "A Pokémon can carry more than one type, so these add up past the Pokémon count.",
    barRows(types,
      (bucket) => displayType(bucket.value),
      (bucket) => ({ dotClass: `type-${bucket.value.toLowerCase()}` })));
}

// Card class and rarity share a block: three rows that account for every card,
// then the rarity head with its long tail folded into one line.
function classAndRarityChart(supertype, rarity) {
  const body = element("div", "chart-split");
  body.append(barRows(supertype));
  const head = rarity.slice(0, RARITY_ROWS);
  body.append(element("p", "chart-subtitle", `Top ${head.length} rarities`));
  body.append(barRows(head));
  const rest = rarity.length - head.length;
  if (rest > 0) {
    body.append(element("p", "chart-note",
      `…and ${rest} more rarities, from Amazing Rare down to the one-off promos.`));
  }
  return chartBlock("Card class and rarity", "Every card has exactly one class.", body);
}

// The superlatives line needs one thing /api/stats cannot answer: the name of
// the card holding the maximum HP. That is one extra page_size=1 search, and
// it degrades to the bare number if it fails.
async function renderSuperlatives(data) {
  const facts = [
    `Highest HP ${data.max_hp}`,
    biggest(data.series, "Biggest series"),
    biggest(data.per_year, "Busiest year", (bucket) => bucket.year),
  ];
  paintSuperlatives(facts);
  try {
    const card = await fetchTopCard("hp");
    if (!card) return;
    facts[0] = `Highest HP ${data.max_hp} — ${card.name}`;
    paintSuperlatives(facts);
  } catch {
    // The number alone is already on screen; a missing name is not an error.
  }
}

function paintSuperlatives(facts) {
  const line = $("stats-superlatives");
  line.replaceChildren(...facts.filter(Boolean).map((fact) => element("span", "stats-fact", fact)));
}

function biggest(buckets, label, name = (bucket) => bucket.value) {
  if (!buckets?.length) return "";
  const top = buckets.reduce((best, bucket) => (bucket.count > best.count ? bucket : best), buckets[0]);
  return `${label} ${name(top)} (${Number(top.count).toLocaleString()})`;
}
