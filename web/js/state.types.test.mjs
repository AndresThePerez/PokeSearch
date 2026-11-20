import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { ENERGY_TYPES, TYPE_ALIASES } from "./state.js";

// The page keeps its own copy of the server's energy types and their two
// aliases, to tell which types members a search used. This reads the
// server's source so the two copies cannot drift apart unnoticed.
const params = readFileSync(new URL("../../internal/search/params.go", import.meta.url), "utf8")
  .replace(/\/\*[\s\S]*?\*\//g, "")
  .replace(/\/\/.*$/gm, "");

function goStrings(body) {
  return [...body.matchAll(/"((?:[^"\\]|\\.)*)"/g)].map((match) => match[1]);
}

test("the page's energy types are the server's, in the server's order", () => {
  const body = params.match(/var CanonicalTypes = \[\]string\{([^}]*)\}/)?.[1];
  assert.ok(body, "CanonicalTypes is in internal/search/params.go");
  assert.deepStrictEqual([...ENERGY_TYPES], goStrings(body));
});

test("the page's type aliases are the server's", () => {
  const body = params.match(/var typeAliases = map\[string\]string\{([^}]*)\}/)?.[1];
  assert.ok(body, "typeAliases is in internal/search/params.go");
  const names = goStrings(body);
  const server = {};
  for (let at = 0; at < names.length; at += 2) server[names[at]] = names[at + 1];
  assert.deepStrictEqual({ ...TYPE_ALIASES }, server);
});
