// The observability rail: the stats grid, the SLA readouts, the service-status
// pill, and the two inspector panes that show the generated DSL and the raw
// response. Imports util only.

import { $ } from "./util.js";

export function renderInspector(dsl, response) {
  $("dsl-json").textContent = dsl ? JSON.stringify(dsl, null, 2) : "No query available.";
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
