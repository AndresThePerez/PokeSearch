import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { noticeView } from "./api.js";

test("an empty notice shows no box and no focusable Dismiss button", () => {
  assert.deepStrictEqual(noticeView(""), { text: "", shown: false, dismissHidden: true });
});

test("a notice with text shows the box and its Dismiss button", () => {
  assert.deepStrictEqual(noticeView('Ignored "sort=bogus": not a valid value.'), {
    text: 'Ignored "sort=bogus": not a valid value.',
    shown: true,
    dismissHidden: false,
  });
});

test("a missing or blank notice counts as empty", () => {
  for (const text of [undefined, null, "   "]) {
    assert.deepStrictEqual(noticeView(text), { text: "", shown: false, dismissHidden: true }, String(text));
  }
});

// The live region has to be in the accessibility tree before its text
// changes, or a screen reader may never announce it. So the markup must never
// hide the region itself, nor the box around it; only the Dismiss button
// starts hidden, so it cannot take focus while there is nothing to dismiss.
test("the notice's live region is rendered from the start and only the button starts hidden", () => {
  const html = readFileSync(new URL("../index.html", import.meta.url), "utf8");
  const box = html.match(/<div id="search-notice"[^>]*>/);
  assert.ok(box, "index.html has no #search-notice box");
  assert.ok(!/\shidden\b/.test(box[0]), `the box starts hidden: ${box[0]}`);
  const region = html.match(/<p id="search-notice-text"[^>]*>/);
  assert.ok(region, "index.html has no #search-notice-text");
  assert.ok(/role="status"/.test(region[0]), "the text element is not a status region");
  assert.ok(!/\shidden\b/.test(region[0]), `the region starts hidden: ${region[0]}`);
  const button = html.match(/<button id="search-notice-dismiss"[^>]*>/);
  assert.ok(button && /\shidden\b/.test(button[0]), "the Dismiss button must start hidden");
});

test("the empty notice's box takes no space in the stylesheet", () => {
  const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
  assert.match(css, /#search-notice:not\(\.is-shown\)\s*\{[^}]*padding:\s*0;[^}]*\}/);
  assert.match(css, /#search-notice:not\(\.is-shown\)\s*\{[^}]*margin:\s*0;[^}]*\}/);
  assert.match(css, /#search-notice:not\(\.is-shown\)\s*\{[^}]*border:\s*0;[^}]*\}/);
});
