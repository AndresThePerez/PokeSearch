import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// The count line's text before the first answer. Beside a sort control that
// shows its order toggle on a phone, a loading text longer than the count it
// gives way to put the sort control on a second row until the count landed,
// and the grid moved up with it.
const page = readFileSync(new URL("../index.html", import.meta.url), "utf8");
const placeholder = page.match(/<p id="total-count"[^>]*>([^<]*)<\/p>/)?.[1];

test("the count line starts with a loading text no longer than the archive's count", () => {
  assert.ok(placeholder, "the count line is in the markup with a text");
  assert.match(placeholder, /^Loading/);
  const longestCount = `${(20635).toLocaleString("en-US")} cards`;
  assert.ok(Array.from(placeholder).length < Array.from(longestCount).length,
    `"${placeholder}" is shorter than "${longestCount}"`);
});
