import test from "node:test";
import assert from "node:assert/strict";
import { queryPanelAction } from "./api.js";

// Opening the query panel: what it takes to show the query for the search on
// screen. The cards on screen are never searched again for it.

test("a panel opened over an answer that carried no query asks for the query alone", () => {
  assert.equal(queryPanelAction({ open: true, hadQuery: false, searching: false }), "query-only");
});

test("a panel opened while a search is still on its way waits for that search, which asks for the query itself", () => {
  assert.equal(queryPanelAction({ open: true, hadQuery: false, searching: true }), "search");
});

test("closing the panel, or opening it over a query already shown, asks for nothing", () => {
  assert.equal(queryPanelAction({ open: false, hadQuery: false, searching: false }), "none");
  assert.equal(queryPanelAction({ open: false, hadQuery: true, searching: true }), "none");
  assert.equal(queryPanelAction({ open: true, hadQuery: true, searching: false }), "none");
  assert.equal(queryPanelAction({ open: true, hadQuery: true, searching: true }), "none");
});

test("anything that is not exactly true counts as false", () => {
  assert.equal(queryPanelAction({}), "none");
  assert.equal(queryPanelAction(), "none");
  assert.equal(queryPanelAction({ open: "yes", hadQuery: 0, searching: 0 }), "none");
});
