import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";

// The page, its styles and its scripts as text, each with its comments taken
// out first, so that a comment naming a tag, a rule or a figure can neither
// satisfy a check nor fail one.
const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const page = read("../index.html").replace(/<!--[\s\S]*?-->/g, "");
const styles = read("../styles.css").replace(/\/\*[\s\S]*?\*\//g, "");
const scripts = readdirSync(new URL(".", import.meta.url))
  .filter((name) => name.endsWith(".js"))
  .map((name) => [name, read(`./${name}`).replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "")]);

const body = page.slice(page.indexOf("<body"), page.indexOf("</body>"));

test("the footer is a child of the page body, after the main column and outside it and the dialog", () => {
  const footers = [...body.matchAll(/<footer\b/g)];
  assert.equal(footers.length, 1, "one footer");
  const at = footers[0].index;
  const mainOpen = body.indexOf("<main");
  const mainClose = body.indexOf("</main>");
  assert.ok(mainOpen !== -1 && mainClose !== -1);
  assert.ok(at > mainClose, "after </main>");
  assert.ok(at < body.indexOf("<dialog"), "not inside the card dialog");
  assert.ok(body.indexOf("</footer>") < body.indexOf("<dialog"));
  assert.match(body.slice(at, at + 60), /class="[^"]*\bsite-footer\b/, "the class the drawer makes inert");
});

test("the availability figure and its label are gone from the markup, the styles and the scripts", () => {
  assert.doesNotMatch(page, /99\.9|Availability|monthly target/i);
  assert.doesNotMatch(styles, /99\.9|availability/i);
  for (const [name, source] of scripts) assert.doesNotMatch(source, /99\.9|availability/i, name);
});

test("no card or set count is written into the page body or any script", () => {
  for (const pattern of [/20,?635/, /\b176\b/]) {
    assert.doesNotMatch(body, pattern);
    for (const [name, source] of scripts) assert.doesNotMatch(source, pattern, name);
  }
});

test("every id in the page is used once", () => {
  const ids = [...page.matchAll(/\sid="([^"]+)"/g)].map((match) => match[1]);
  const repeated = ids.filter((id, index) => ids.indexOf(id) !== index);
  assert.deepStrictEqual(repeated, []);
});

test("every link that opens a new tab keeps the opener away from the page it opens", () => {
  const blank = [...body.matchAll(/<a\b[^>]*\btarget="_blank"[^>]*>/g)].map((match) => match[0]);
  assert.ok(blank.length >= 3);
  for (const tag of blank) {
    const rel = tag.match(/\brel="([^"]*)"/);
    assert.ok(rel, tag);
    assert.ok(rel[1].split(/\s+/).includes("noopener"), tag);
  }
});

// The empty state's examples must be out of the tab order until a search finds
// nothing. They are taken out by the hidden attribute on the guide and on the
// empty state around it, and a class that sets display cannot bring them back
// because the page's [hidden] rule is !important.
test("the hidden examples cannot be reached by keyboard while their state is not shown", () => {
  const openTag = (id) => body.match(new RegExp(`<[a-z]+\\b[^>]*\\bid="${id}"[^>]*>`))?.[0] ?? "";
  for (const id of ["empty-state", "empty-guide", "badge-legend"]) assert.match(openTag(id), /\shidden(\s|>|=)/, id);
  const guide = body.indexOf('id="empty-guide"');
  assert.ok(guide > body.indexOf('id="empty-state"') && guide < body.indexOf('id="load-more"'), "inside the empty state");
  assert.match(styles, /\[hidden\]\s*\{\s*display:\s*none\s*!important;?\s*\}/);
  for (const id of ["landing-examples", "empty-examples", "badge-legend"]) {
    const content = body.match(new RegExp(`id="${id}"[^>]*>([\\s\\S]*?)</div>`))?.[1];
    assert.equal(content?.trim(), "", `${id} ships empty; its buttons come from the one list in the scripts`);
  }
  assert.doesNotMatch(body, /data-q=/, "no example query is written twice");
});
