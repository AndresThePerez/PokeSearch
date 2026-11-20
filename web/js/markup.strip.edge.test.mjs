import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// The strip above the grid, read as text and held to stricter rules than
// "the words are somewhere": where it sits in the tree, which widths show it,
// that the ids the scripts use exist, and that the two modules which share the
// grid's loading class agree on its name. Comments are taken out first.
const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const page = read("../index.html").replace(/<!--[\s\S]*?-->/g, "");
const styles = read("../styles.css").replace(/\/\*[\s\S]*?\*\//g, "");
const code = (path) => read(path).replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");

const VOID = new Set(["area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source", "track", "wbr"]);

// parse reads the page into elements with their ancestors, enough for the
// questions below. It handles the markup this page has: no unquoted
// attributes, and script and style bodies skipped whole.
function parse(source) {
  const elements = [];
  const stack = [];
  const body = source.replace(/<(script|style)\b[\s\S]*?<\/\1>/g, "");
  for (const match of body.matchAll(/<(\/?)([a-zA-Z][a-zA-Z0-9-]*)((?:\s+[^\s=>]+(?:="[^"]*")?)*)\s*(\/?)>/g)) {
    const [whole, closing, rawName, rawAttrs, selfClosing] = match;
    const name = rawName.toLowerCase();
    if (closing) {
      const at = stack.map((el) => el.name).lastIndexOf(name);
      if (at !== -1) stack.length = at;
      continue;
    }
    const attrs = {};
    for (const a of rawAttrs.matchAll(/([^\s=>]+)(?:="([^"]*)")?/g)) attrs[a[1]] = a[2] ?? "";
    const el = { name, attrs, ancestors: [...stack], index: match.index, end: match.index + whole.length, id: attrs.id ?? null };
    elements.push(el);
    if (!VOID.has(name) && !selfClosing) stack.push(el);
  }
  return elements;
}

const tree = parse(page);
const byID = (id) => tree.filter((el) => el.id === id);
const only = (id) => {
  assert.equal(byID(id).length, 1, `${id} must exist once`);
  return byID(id)[0];
};
const classes = (el) => (el.attrs.class ?? "").split(/\s+/);

function mediaBlocks() {
  const blocks = [];
  let rest = "";
  let from = 0;
  for (let start = styles.indexOf("@media", from); start !== -1; start = styles.indexOf("@media", from)) {
    const open = styles.indexOf("{", start);
    let depth = 0;
    let at = open;
    for (; at < styles.length; at++) {
      if (styles[at] === "{") depth++;
      if (styles[at] === "}" && --depth === 0) break;
    }
    rest += styles.slice(from, start);
    blocks.push([styles.slice(start + 6, open).trim(), styles.slice(open + 1, at)]);
    from = at + 1;
  }
  return { blocks, topLevel: rest + styles.slice(from) };
}

// displays lists the display value each rule in source sets for a selector.
function displays(selector, source) {
  const found = [];
  for (const match of source.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = match[1].split(",").map((part) => part.trim().replace(/\s+/g, " "));
    if (!selectors.includes(selector)) continue;
    for (const d of match[2].matchAll(/(?:^|;|\s)display:\s*([a-z-]+)/g)) found.push(d[1]);
  }
  return found;
}

test("every id in the page is unique", () => {
  const seen = new Map();
  for (const el of tree) if (el.id !== null) seen.set(el.id, (seen.get(el.id) ?? 0) + 1);
  const twice = [...seen].filter(([, count]) => count > 1).map(([id]) => id);
  assert.deepStrictEqual(twice, []);
});

test("the strip is a plain block in the main column, ahead of the grid, with nothing around it that hides it", () => {
  const strip = only("search-strip");
  const grid = only("results-grid");
  const main = tree.find((el) => el.name === "main");
  assert.ok(strip.ancestors.includes(main), "inside main");
  assert.ok(strip.index < grid.index, "before the grid");
  assert.ok(!grid.ancestors.includes(strip) && !strip.ancestors.includes(grid), "not nested with the grid");
  for (const ancestor of strip.ancestors) {
    for (const attribute of ["hidden", "inert", "aria-hidden"]) {
      assert.ok(!(attribute in ancestor.attrs), `${ancestor.name} carries ${attribute}`);
    }
    assert.ok(!["details", "dialog", "template", "ul", "ol", "table", "form", "nav", "aside"].includes(ancestor.name), ancestor.name);
  }
  assert.equal(strip.attrs["aria-busy"], "false", "it starts idle");
});

test("exactly one media query shows the strip, and it is max-width 1050px with no lower bound", () => {
  const { blocks, topLevel } = mediaBlocks();
  for (const selector of [".search-strip", ".rail-back"]) {
    const showing = blocks.filter(([, body]) => displays(selector, body).some((value) => value !== "none"));
    assert.deepStrictEqual(showing.map(([query]) => query), ["(max-width: 1050px)"], selector);
    assert.ok(displays(selector, topLevel).length > 0 && displays(selector, topLevel).every((value) => value === "none"), `${selector} is hidden outside the query`);
    // No other query puts it away again at a narrower width, where it would vanish on the phones that need it.
    for (const [query, body] of blocks) {
      if (query === "(max-width: 1050px)") continue;
      assert.ok(displays(selector, body).every((value) => value === "none") || displays(selector, body).length === 0, `${selector} in ${query}`);
    }
  }
});

test("the stats route puts the strip and the way back away with a rule that outranks the media query's", () => {
  const specificity = (selector) => ({
    ids: (selector.match(/#/g) ?? []).length,
    classes: (selector.match(/\.[a-z]|\[|:[a-z]/g) ?? []).length,
    types: (selector.match(/(^|[\s>+~])[a-z]/g) ?? []).length,
  });
  const rank = ({ ids, classes: c, types }) => ids * 10000 + c * 100 + types;
  for (const target of [".search-strip", ".rail-back"]) {
    const route = `body.route-stats ${target}`;
    assert.ok(displays(route, styles).includes("none"), route);
    assert.ok(rank(specificity(route)) > rank(specificity(target)), `${route} must outrank ${target}`);
  }
});

test("the way back is a button, first in the rail, ahead of everything else the keyboard can reach there", () => {
  const rail = tree.find((el) => el.name === "aside" && classes(el).includes("observability-rail"));
  assert.ok(rail, "no rail");
  const back = only("rail-back");
  assert.ok(back.ancestors.includes(rail));
  assert.equal(back.name, "button");
  assert.equal(back.attrs.type, "button");
  for (const attribute of ["hidden", "disabled"]) assert.ok(!(attribute in back.attrs), attribute);
  const reachable = tree.filter((el) => el.ancestors.includes(rail) && ["button", "a", "summary", "input", "select", "textarea"].includes(el.name));
  assert.ok(back === reachable[0], "something in the rail comes before the way back");
});

test("each abbreviation is hidden from screen readers and sits beside a full name that is not", () => {
  const strip = only("search-strip");
  const items = tree.filter((el) => el.ancestors.includes(strip) && classes(el).includes("strip-item"));
  assert.equal(items.length, 3);
  for (const item of items) {
    const parts = tree.filter((el) => el.ancestors.includes(item));
    const shortForms = parts.filter((el) => el.attrs["aria-hidden"] === "true");
    const fullNames = parts.filter((el) => classes(el).includes("sr-only"));
    assert.ok(shortForms.length >= 1, "no short form");
    assert.equal(fullNames.length, 1, "one full name per item");
    assert.ok(fullNames.every((el) => !("aria-hidden" in el.attrs) && !("hidden" in el.attrs)));
    assert.ok(fullNames[0].id.endsWith("-name"));
    assert.ok(shortForms.some((el) => `${el.id}-name` === fullNames[0].id), "the number and its name are paired by id");
  }
  const labels = tree.filter((el) => classes(el).includes("strip-label"));
  assert.ok(labels.length > 0 && labels.every((el) => el.attrs["aria-hidden"] === "true"), "a visible label is read twice");
});

test("nothing in the strip announces by itself, and its controls are plain buttons", () => {
  const strip = only("search-strip");
  const inside = tree.filter((el) => el.ancestors.includes(strip));
  for (const el of [strip, ...inside]) {
    assert.ok(!("aria-live" in el.attrs), `${el.id ?? el.name} has aria-live`);
    assert.ok(!["status", "alert", "log", "marquee", "timer"].includes(el.attrs.role), `${el.id ?? el.name} has role ${el.attrs.role}`);
    assert.ok(!("tabindex" in el.attrs) || Number(el.attrs.tabindex) < 0, `${el.id ?? el.name} adds a tab stop`);
  }
  const buttons = inside.filter((el) => el.name === "button");
  assert.deepStrictEqual(buttons.map((el) => el.id), ["view-query"]);
  for (const el of [...buttons, only("rail-back")]) {
    assert.equal(el.attrs.type, "button", el.id);
    assert.ok(!el.ancestors.some((ancestor) => ancestor.name === "form" || ancestor.name === "a"), `${el.id} sits in a form or link`);
    assert.ok(!("disabled" in el.attrs) && !("hidden" in el.attrs), el.id);
  }
});

test("the rail's heading stays an h2 the section is named by, and a script can focus it but Tab does not", () => {
  const heading = only("telemetry-heading");
  assert.equal(heading.name, "h2");
  assert.equal(heading.attrs.tabindex, "-1");
  const section = tree.find((el) => el.attrs["aria-labelledby"] === "telemetry-heading");
  assert.ok(section, "nothing is named by the heading");
  assert.ok(heading.ancestors.includes(section));
});

test("every strip id the scripts look up is in the page once", () => {
  const used = new Set();
  for (const file of ["./telemetry.js", "./main.js"]) {
    for (const match of code(file).matchAll(/\$\("((?:strip-|search-strip|view-query|rail-back|telemetry-heading)[a-z-]*)"\)/g)) used.add(match[1]);
  }
  for (const id of ["search-strip", "strip-es", "strip-es-name", "strip-roundtrip", "strip-roundtrip-name", "strip-count", "strip-count-name", "view-query", "rail-back", "telemetry-heading"]) {
    assert.ok(used.has(id), `no script uses ${id}`);
  }
  for (const id of used) assert.equal(byID(id).length, 1, `${id} is used by a script and must be in the page once`);
});

test("the strip is told about searches by the class the grid really carries", () => {
  const observed = code("./main.js").match(/classList\.contains\("([a-z-]+)"\)/g) ?? [];
  const names = observed.map((hit) => hit.match(/"([a-z-]+)"/)[1]);
  assert.ok(names.includes("is-loading"), "main.js does not watch is-loading");
  assert.match(code("./render.js"), /grid\.classList\.add\("is-loading"\)/);
  assert.match(code("./render.js"), /grid\.classList\.remove\("is-loading"\)/);
  const watch = code("./main.js").match(/function watchSearchLoading\(\) \{[\s\S]*?\n\}/)?.[0] ?? "";
  assert.match(watch, /\$\("results-grid"\)/);
  assert.match(watch, /attributeFilter:\s*\["class"\]/);
  assert.match(watch, /notify\(\);\s*\n\}$/, "it reads the class once at start");
});
