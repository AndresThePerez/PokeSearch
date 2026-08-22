// The observability rail: the stats grid, the SLA readouts, the service-status
// pill, and the two inspector panes that show the generated DSL and the raw
// response. Imports util only.

import { $ } from "./util.js";

let hadDSL = false;

// debugEnabled gates the debug=1 request parameter on whether anyone is
// actually looking. The DSL is the largest part of a search response and it
// used to be requested on every keystroke, open panel or not.
export function debugEnabled() {
  return $("query-inspector").open;
}

// lastResponseHadDSL lets main.js decide whether opening the panel needs a
// re-fetch or whether the DSL is already on screen.
export function lastResponseHadDSL() {
  return hadDSL;
}

export function renderInspector(dsl, response) {
  hadDSL = Boolean(dsl);
  $("dsl-json").textContent = dsl
    ? JSON.stringify(dsl, null, 2)
    : "Open this panel to capture the query — the next search will return its DSL.";
  $("response-json").textContent = response ? JSON.stringify(response, null, 2) : "No response available.";
}

export function setDegraded(isDegraded) {
  $("degraded-banner").hidden = !isDegraded;
  setServiceStatus(isDegraded ? "Degraded" : "Online", isDegraded);
}

export function setServiceStatus(label, degraded = false) {
  $("service-status").lastChild.textContent = label;
  $("service-status").classList.toggle("is-degraded", degraded);
}

export function renderStats(data, roundTripMs) {
  $("stat-results").textContent = Number(data.total).toLocaleString();
  $("stat-engine").textContent = `${data.took_ms ?? "—"} ms`;
  $("stat-roundtrip").textContent = `${Math.round(roundTripMs)} ms`;
  $("stat-page").textContent = `${data.page} / ${data.pages || 0}`;
  $("sla-engine-state").textContent = data.took_ms < 100 ? "within target" : "above target";
  $("sla-roundtrip-state").textContent = roundTripMs < 250 ? "within target" : "above target";
}
