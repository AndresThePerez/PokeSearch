import test from "node:test";
import assert from "node:assert/strict";
import {
  acceptedFilters,
  addressWithFilters,
  rewriteFilterAddress,
  readStateFromURL,
  state,
  ENERGY_TYPES,
} from "./state.js";

// The filters a search used, read against what the server did with the same
// addresses. The answers below were taken from a running server (the filter
// its query was built with, from the debug output, and the total); the
// page's reading of an address has to name the same filters, or the chips
// show a filter the search did not use, drop one it did, or the rewritten
// address asks for something else than the search that was run.

const STORED_RARITIES = [
  "ACE SPEC Rare", "Amazing Rare", "Black White Rare", "Classic Collection", "Common", "Double Rare",
  "Futuristic Rare", "Holo Rare V", "Holo Rare VMAX", "Holo Rare VSTAR", "Hyper Rare", "Illustration Rare",
  "LEGEND", "MEGA_ATTACK_RARE", "Mega Hyper Rare", "Pikachu Rare", "Promo", "Radiant Rare", "Rare", "Rare ACE",
  "Rare BREAK", "Rare Holo", "Rare Holo EX", "Rare Holo GX", "Rare Holo LV.X", "Rare Holo Star", "Rare Holo V",
  "Rare Holo VMAX", "Rare Holo VSTAR", "Rare Holo ex", "Rare Prime", "Rare Prism Star", "Rare Rainbow",
  "Rare Secret", "Rare Shining", "Rare Shiny", "Rare Shiny GX", "Rare Ultra", "Shiny Rare", "Shiny Ultra Rare",
  "Special Illustration Rare", "Trainer Gallery Rare Holo", "Ultra Rare", "Uncommon",
];

const allRarities = { rarity: STORED_RARITIES.map((value) => ({ value, count: 10 })) };

// readAddress is the state the page holds for an address, as it reads it.
function readAddress(search) {
  globalThis.location = { search, pathname: "/", hash: "" };
  readStateFromURL();
  return state;
}

const asParam = (name, value) => `?${new URLSearchParams({ [name]: value })}`;
const reading = (text) => JSON.stringify(text).replace(/[^\x20-\x7e]/g, (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`);

// Letters and spaces the page and the server do not fold or trim alike. They
// have their own tests below; the sweeps leave them out.
const UNEVEN = /[﻿\u0085ſKİ]/u;


const TYPE_ANSWERS = [
  ["Colorless", ["Colorless"]],
  ["colorless", ["Colorless"]],
  ["COLORLESS", ["Colorless"]],
  ["cOLORLESS", ["Colorless"]],
  [" Colorless", ["Colorless"]],
  ["Colorless ", ["Colorless"]],
  ["\tColorless\t", ["Colorless"]],
  ["\u00a0Colorless\u00a0", ["Colorless"]],
  ["Colorless,Colorless", ["Colorless"]],
  ["Colorless,colorless", ["Colorless"]],
  ["Darkness", ["Darkness"]],
  ["darkness", ["Darkness"]],
  ["DARKNESS", ["Darkness"]],
  ["dARKNESS", ["Darkness"]],
  [" Darkness", ["Darkness"]],
  ["Darkness ", ["Darkness"]],
  ["\tDarkness\t", ["Darkness"]],
  ["\u00a0Darkness\u00a0", ["Darkness"]],
  ["Darkness,Darkness", ["Darkness"]],
  ["Darkness,darkness", ["Darkness"]],
  ["Dragon", ["Dragon"]],
  ["dragon", ["Dragon"]],
  ["DRAGON", ["Dragon"]],
  ["dRAGON", ["Dragon"]],
  [" Dragon", ["Dragon"]],
  ["Dragon ", ["Dragon"]],
  ["\tDragon\t", ["Dragon"]],
  ["\u00a0Dragon\u00a0", ["Dragon"]],
  ["Dragon,Dragon", ["Dragon"]],
  ["Dragon,dragon", ["Dragon"]],
  ["Fairy", ["Fairy"]],
  ["fairy", ["Fairy"]],
  ["FAIRY", ["Fairy"]],
  ["fAIRY", ["Fairy"]],
  [" Fairy", ["Fairy"]],
  ["Fairy ", ["Fairy"]],
  ["\tFairy\t", ["Fairy"]],
  ["\u00a0Fairy\u00a0", ["Fairy"]],
  ["Fairy,Fairy", ["Fairy"]],
  ["Fairy,fairy", ["Fairy"]],
  ["Fighting", ["Fighting"]],
  ["fighting", ["Fighting"]],
  ["FIGHTING", ["Fighting"]],
  ["fIGHTING", ["Fighting"]],
  [" Fighting", ["Fighting"]],
  ["Fighting ", ["Fighting"]],
  ["\tFighting\t", ["Fighting"]],
  ["\u00a0Fighting\u00a0", ["Fighting"]],
  ["Fighting,Fighting", ["Fighting"]],
  ["Fighting,fighting", ["Fighting"]],
  ["Fire", ["Fire"]],
  ["fire", ["Fire"]],
  ["FIRE", ["Fire"]],
  ["fIRE", ["Fire"]],
  [" Fire", ["Fire"]],
  ["Fire ", ["Fire"]],
  ["\tFire\t", ["Fire"]],
  ["\u00a0Fire\u00a0", ["Fire"]],
  ["Fire,Fire", ["Fire"]],
  ["Fire,fire", ["Fire"]],
  ["Grass", ["Grass"]],
  ["grass", ["Grass"]],
  ["GRASS", ["Grass"]],
  ["gRASS", ["Grass"]],
  [" Grass", ["Grass"]],
  ["Grass ", ["Grass"]],
  ["\tGrass\t", ["Grass"]],
  ["\u00a0Grass\u00a0", ["Grass"]],
  ["Grass,Grass", ["Grass"]],
  ["Grass,grass", ["Grass"]],
  ["Lightning", ["Lightning"]],
  ["lightning", ["Lightning"]],
  ["LIGHTNING", ["Lightning"]],
  ["lIGHTNING", ["Lightning"]],
  [" Lightning", ["Lightning"]],
  ["Lightning ", ["Lightning"]],
  ["\tLightning\t", ["Lightning"]],
  ["\u00a0Lightning\u00a0", ["Lightning"]],
  ["Lightning,Lightning", ["Lightning"]],
  ["Lightning,lightning", ["Lightning"]],
  ["Metal", ["Metal"]],
  ["metal", ["Metal"]],
  ["METAL", ["Metal"]],
  ["mETAL", ["Metal"]],
  [" Metal", ["Metal"]],
  ["Metal ", ["Metal"]],
  ["\tMetal\t", ["Metal"]],
  ["\u00a0Metal\u00a0", ["Metal"]],
  ["Metal,Metal", ["Metal"]],
  ["Metal,metal", ["Metal"]],
  ["Psychic", ["Psychic"]],
  ["psychic", ["Psychic"]],
  ["PSYCHIC", ["Psychic"]],
  ["pSYCHIC", ["Psychic"]],
  [" Psychic", ["Psychic"]],
  ["Psychic ", ["Psychic"]],
  ["\tPsychic\t", ["Psychic"]],
  ["\u00a0Psychic\u00a0", ["Psychic"]],
  ["Psychic,Psychic", ["Psychic"]],
  ["Psychic,psychic", ["Psychic"]],
  ["Water", ["Water"]],
  ["water", ["Water"]],
  ["WATER", ["Water"]],
  ["wATER", ["Water"]],
  [" Water", ["Water"]],
  ["Water ", ["Water"]],
  ["\tWater\t", ["Water"]],
  ["\u00a0Water\u00a0", ["Water"]],
  ["Water,Water", ["Water"]],
  ["Water,water", ["Water"]],
  ["Steel", ["Metal"]],
  ["steel", ["Metal"]],
  ["STEEL", ["Metal"]],
  ["Normal", ["Colorless"]],
  ["normal", ["Colorless"]],
  ["NORMAL", ["Colorless"]],
  ["Steel,Metal", ["Metal"]],
  ["Metal,Steel", ["Metal"]],
  ["Normal,Colorless", ["Colorless"]],
  [" Steel ", ["Metal"]],
  ["Fir", []],
  ["Fire ", ["Fire"]],
  ["Fire,", ["Fire"]],
  [",Fire", ["Fire"]],
  ["Fire,,Water", ["Fire","Water"]],
  ["Fire, ,Water", ["Fire","Water"]],
  ["", []],
  [",", []],
  [" ", []],
  ["Fire,Bogus", ["Fire"]],
  ["Bogus,Fire", ["Fire"]],
  ["Bogus", []],
  ["\u017fteel", []],
  ["\uff26\uff49\uff52\uff45", []],
  ["f\u0131re", []],
  ["Fi\u0307re", []],
  ["F\u0130re", []],
  ["Gra\u017fs", ["Grass"]],
  ["Darkne\u017fs", ["Darkness"]],
  ["P\u017fychic", ["Psychic"]],
  ["DarKness", ["Darkness"]],
  ["\ufeffFire", []],
  ["\u0085Fire", ["Fire"]],
  ["Fire\u0085", ["Fire"]],
  ["\u2003Fire\u2003", ["Fire"]],
  ["\u200bFire", []],
  ["Fire\u200b", []],
  ["Steel\u00a0", ["Metal"]],
  ["Fire\u0000", []],
  ["Lightning,Fighting,Fairy,Dragon,Darkness,Colorless", ["Lightning","Fighting","Fairy","Dragon","Darkness","Colorless"]],
  ["constructor", []],
  ["__proto__", []],
  ["toString", []],
  ["Metal,constructor", ["Metal"]],
];
const RARITY_GRID_ANSWERS = [0,0,0,0,0,0,0,0,0,0,["ACE_SPEC_Rare"],1,1,1,1,1,1,1,1,1,1,["Amazing_Rare"],2,2,2,2,2,2,2,2,2,2,["Black_White_Rare"],3,3,3,3,3,3,3,3,3,3,["Classic_Collection"],4,4,4,4,4,4,4,4,4,4,4,5,5,5,5,5,5,5,5,5,5,["Double_Rare"],6,6,6,6,6,6,6,6,6,6,["Futuristic_Rare"],7,7,7,7,7,7,7,7,7,7,["Holo_Rare_V"],8,8,8,8,8,8,8,8,8,8,["Holo_Rare_VMAX"],9,9,9,9,9,9,9,9,9,9,["Holo_Rare_VSTAR"],10,10,10,10,10,10,10,10,10,10,["Hyper_Rare"],11,11,11,11,11,11,11,11,11,11,["Illustration_Rare"],12,12,12,12,12,12,12,12,12,12,12,13,13,13,13,13,13,13,13,13,["MEGA ATTACK RARE"],13,14,14,14,14,14,14,14,14,14,14,["Mega_Hyper_Rare"],15,15,15,15,15,15,15,15,15,15,["Pikachu_Rare"],16,16,16,16,16,16,16,16,16,16,16,17,17,17,17,17,17,17,17,17,17,["Radiant_Rare"],18,18,18,18,18,18,18,18,18,18,18,19,19,19,19,19,19,19,19,19,19,["Rare_ACE"],20,20,20,20,20,20,20,20,20,20,["Rare_BREAK"],21,21,21,21,21,21,21,21,21,21,["Rare_Holo"],22,["rare holo ex"],["RARE HOLO EX"],["Rare  Holo  EX"],["Rare\tHolo\tEX"],["Rare Holo EX"],22,22,["Rare Holo EX"],22,["Rare_Holo_EX"],23,23,23,23,23,23,23,23,23,23,["Rare_Holo_GX"],24,24,24,24,24,24,24,24,24,24,["Rare_Holo_LV.X"],25,25,25,25,25,25,25,25,25,25,["Rare_Holo_Star"],26,26,26,26,26,26,26,26,26,26,["Rare_Holo_V"],27,27,27,27,27,27,27,27,27,27,["Rare_Holo_VMAX"],28,28,28,28,28,28,28,28,28,28,["Rare_Holo_VSTAR"],29,["rare holo ex"],["RARE HOLO EX"],["Rare  Holo  ex"],["Rare\tHolo\tex"],["Rare Holo ex"],29,29,["Rare Holo ex"],29,["Rare_Holo_ex"],30,30,30,30,30,30,30,30,30,30,["Rare_Prime"],31,31,31,31,31,31,31,31,31,31,["Rare_Prism_Star"],32,32,32,32,32,32,32,32,32,32,["Rare_Rainbow"],33,33,33,33,33,33,33,33,33,33,["Rare_Secret"],34,34,34,34,34,34,34,34,34,34,["Rare_Shining"],35,35,35,35,35,35,35,35,35,35,["Rare_Shiny"],36,36,36,36,36,36,36,36,36,36,["Rare_Shiny_GX"],37,37,37,37,37,37,37,37,37,37,["Rare_Ultra"],38,38,38,38,38,38,38,38,38,38,["Shiny_Rare"],39,39,39,39,39,39,39,39,39,39,["Shiny_Ultra_Rare"],40,40,40,40,40,40,40,40,40,40,["Special_Illustration_Rare"],41,41,41,41,41,41,41,41,41,41,["Trainer_Gallery_Rare_Holo"],42,42,42,42,42,42,42,42,42,42,["Ultra_Rare"],43,43,43,43,43,43,43,43,43,43,43];
const RARITY_EXTRA_ANSWERS = [
  ["rare holo ex", 0, ["rare holo ex"]],
  ["RARE HOLO EX", 0, ["RARE HOLO EX"]],
  ["Rare Holo EX", 321, ["Rare Holo EX"]],
  ["Rare Holo ex", 1, ["Rare Holo ex"]],
  ["rare  holo  ex", 0, ["rare  holo  ex"]],
  ["", 20635, []],
  ["  ", 20635, []],
  ["bogus", 0, ["bogus"]],
  ["Rare,Common", 7990, ["Rare","Common"]],
  ["rare, common", 7990, ["Rare","Common"]],
  ["Rare,Bogus", 2583, ["Rare","Bogus"]],
  [",", 20635, []],
  ["Rare,", 2583, ["Rare"]],
  ["  ,  ", 20635, []],
  ["_", 0, ["_"]],
  ["\u00a0", 20635, []],
  ["Rare Sh\u0130ny", 149, ["Rare Shiny"]],
  ["rare\u0085holo", 1621, ["Rare Holo"]],
  ["rare\ufeffholo", 0, ["rare\ufeffholo"]],
  ["\ufeffRare Holo", 0, ["\ufeffRare Holo"]],
  ["Rare Holo\ufeff", 0, ["Rare Holo\ufeff"]],
  ["Rare\u0085Holo", 1621, ["Rare Holo"]],
  ["Rare\u2028Holo", 1621, ["Rare Holo"]],
  ["Rare\u180eHolo", 0, ["Rare\u180eHolo"]],
  ["\u017fhiny rare", 0, ["\u017fhiny rare"]],
  ["KINETIC", 0, ["KINETIC"]],
  ["Rare Holo K", 0, ["Rare Holo K"]],
  ["MEGA ATTACK RARE", 0, ["MEGA ATTACK RARE"]],
  ["mega_attack_rare", 7, ["MEGA_ATTACK_RARE"]],
  ["Mega Attack Rare", 0, ["Mega Attack Rare"]],
  ["Legend", 20, ["LEGEND"]],
  ["LEGEND ", 20, ["LEGEND"]],
  ["legend", 20, ["LEGEND"]],
];

test("every types spelling the server was asked is kept or dropped as the server did", () => {
  let checked = 0;
  for (const [text, used] of TYPE_ANSWERS) {
    if (UNEVEN.test(text)) continue;
    const result = acceptedFilters({ state: readAddress(asParam("types", text)), total: 1, facets: {} });
    assert.deepStrictEqual(result.types, used, `types=${reading(text)}`);
    checked++;
  }
  assert.ok(checked > 140, `only ${checked} spellings checked`);
});

test("a types answer the server used is a type the page knows, and the address is rewritten only when it differs", () => {
  for (const [text, used] of TYPE_ANSWERS) {
    if (UNEVEN.test(text)) continue;
    const given = readAddress(asParam("types", text)).types;
    const result = acceptedFilters({ state: state, total: 1, facets: {} });
    for (const type of result.types) assert.ok(ENERGY_TYPES.includes(type), `${type} for ${reading(text)}`);
    assert.equal(result.changed, JSON.stringify(given) !== JSON.stringify(used), `changed for types=${reading(text)}`);
  }
});

test("every rarity spelling of every stored value maps where the server mapped it", () => {
  const variants = 11;
  assert.equal(RARITY_GRID_ANSWERS.length, STORED_RARITIES.length * variants);
  let mapped = 0;
  STORED_RARITIES.forEach((value, at) => {
    const spellings = [
      value, value.toLowerCase(), value.toUpperCase(), value.replace(/ /g, "  "), value.replace(/ /g, "\t"),
      value.replace(/ /g, " "), ` ${value} `, `\t${value} `, value.replace(/ /g, " "),
      value.replace(/_/g, " "), value.replace(/ /g, "_"),
    ];
    spellings.forEach((text, n) => {
      const answer = RARITY_GRID_ANSWERS[at * variants + n];
      const state_ = readAddress(asParam("rarity", text));
      const mappedTo = typeof answer === "number" ? STORED_RARITIES[answer] : null;
      const result = acceptedFilters({ state: state_, total: mappedTo ? 5 : 0, facets: allRarities });
      if (mappedTo) {
        assert.equal(result.rarity, mappedTo, `rarity=${reading(text)}`);
        mapped++;
      } else {
        // The server kept it as given, and it matched nothing: the chip stays.
        assert.equal(result.rarity, text.trim(), `rarity=${reading(text)}`);
      }
    });
  });
  assert.ok(mapped > 400, `only ${mapped} spellings mapped`);
});

test("rarity spellings that are the server's odd cases agree with what it used", () => {
  for (const [text, total, used] of RARITY_EXTRA_ANSWERS) {
    if (UNEVEN.test(text) || !text.replace(/[\s,]/g, "")) continue;
    const result = acceptedFilters({ state: readAddress(asParam("rarity", text)), total, facets: allRarities });
    if (used.length === 1 && !text.includes(",")) {
      // Mapped, kept as given, or the blank that was no filter.
      const exact = used[0] === result.rarity;
      assert.ok(exact || (total === 0 && text.trim() === result.rarity), `rarity=${reading(text)} server ${reading(used)} page ${reading(result.rarity)}`);
    } else {
      // A list the server split: the chip names what was asked, untouched.
      assert.equal(result.rarity, text.trim(), `rarity=${reading(text)}`);
    }
  }
});

// The server trims a list member with the Unicode white space set: it keeps a
// byte order mark where the page's trim removes it, and removes a next-line
// control the page keeps. The page then keeps a chip, and rewrites the address
// to a filter, that the search never used.
test("a byte order mark in front of a type is not a type the page keeps, as the server did not", () => {
  const result = acceptedFilters({ state: readAddress(asParam("types", "﻿Fire")), total: 20635, facets: {} });
  assert.deepStrictEqual(result.types, [], "the server used no types filter for this address");
});

test("a next-line control around a type does not hide the type the server used", () => {
  for (const text of ["\u0085Fire", "Fire\u0085"]) {
    const result = acceptedFilters({ state: readAddress(asParam("types", text)), total: 1586, facets: {} });
    assert.deepStrictEqual(result.types, ["Fire"], `types=${reading(text)}`);
  }
});

test("a byte order mark in front of a stored rarity does not turn a search that found nothing into one that finds cards", () => {
  // The server filtered on the exact text, matched nothing, and the page's
  // answer still listed Rare Holo among the other cards of the search. The
  // address must not be rewritten to the stored value: it would give another
  // search than the one the visitor just saw.
  const result = acceptedFilters({ state: readAddress(asParam("rarity", "﻿Rare Holo")), total: 0, facets: allRarities });
  assert.notEqual(result.rarity, "Rare Holo", "the rewritten address would find 1,621 cards where the search found none");
});

// The server folds letter case with Unicode simple folding (the long s is an s,
// the Kelvin sign a k, a dotted capital I an i in a rarity), and the page
// compares with toLowerCase, which folds none of those.
test("a type spelled with a letter the server folds onto an ASCII one is kept, as the server kept it", () => {
  for (const [text, type] of [["Graſs", "Grass"], ["Darkneſs", "Darkness"], ["Pſychic", "Psychic"], ["DarKness", "Darkness"]]) {
    const result = acceptedFilters({ state: readAddress(asParam("types", text)), total: 1309, facets: {} });
    assert.deepStrictEqual(result.types, [type], `types=${reading(text)}`);
  }
});

test("a rarity spelled with a letter the server folds is mapped onto the stored value it found", () => {
  const result = acceptedFilters({ state: readAddress(asParam("rarity", "Rare Shİny")), total: 149, facets: allRarities });
  assert.equal(result.rarity, "Rare Shiny");
});

// A list with nothing in it is no filter to the server: the search is the
// whole archive. A chip for it says the archive is filtered.
test("a rarity or series made only of separators is no filter and gets no chip", () => {
  for (const text of [",", " , ", "  ,  "]) {
    const rarity = acceptedFilters({ state: readAddress(asParam("rarity", text)), total: 20635, facets: allRarities });
    assert.equal(rarity.rarity, "", `rarity=${reading(text)}`);
    const series = acceptedFilters({ state: readAddress(asParam("series", text)), total: 20635, facets: allRarities });
    assert.equal(series.series, "", `series=${reading(text)}`);
  }
});

// Hostile input: acceptedFilters is called on every first page, so a state
// field of an unexpected kind must not stop the results from painting.
const bare = { q: "", supertype: "", types: [], set: "", rarity: "", series: "", sort: "", order: "", page: 1 };

function deepFreeze(value) {
  if (value && typeof value === "object") {
    for (const key of Object.keys(value)) deepFreeze(value[key]);
    Object.freeze(value);
  }
  return value;
}

test("a state field of an odd kind never throws and never leaves a filter that is not text", () => {
  const odd = [null, undefined, 5, NaN, true, {}, [], ["Rare Holo"], "", " "];
  for (const value of odd) {
    for (const field of ["rarity", "set", "series"]) {
      const result = acceptedFilters({ state: { ...bare, [field]: value }, total: 3, facets: allRarities });
      for (const name of ["rarity", "set", "series"]) assert.equal(typeof result[name], "string", `${field}=${String(value)} gave a ${typeof result[name]} ${name}`);
      assert.ok(Array.isArray(result.types));
    }
    const result = acceptedFilters({ state: { ...bare, types: [value, "Fire", value] }, total: 3, facets: {} });
    assert.deepStrictEqual(result.types, ["Fire"], `types holding ${String(value)}`);
  }
});

test("facets and totals of odd shapes leave a rarity as given instead of throwing", () => {
  const given = { ...bare, rarity: "rare holo" };
  const facetShapes = [undefined, null, 5, "Rare Holo", [], {}, { rarity: null }, { rarity: "Rare Holo" }, { rarity: {} },
    { rarity: [null, 5, "x", {}, { count: 3 }, { value: 5, count: 3 }, { value: "Rare Holo" }, { value: "Rare Holo", count: 0 }, { value: "Rare Holo", count: -4 }, { value: "Rare Holo", count: "NaN" }] }];
  for (const facets of facetShapes) {
    for (const total of [0, -0, NaN, undefined, null, "0", -1, Infinity]) {
      const result = acceptedFilters({ state: given, total, facets });
      assert.equal(result.rarity, "rare holo", `facets ${JSON.stringify(facets)} total ${String(total)}`);
      assert.equal(result.changed, false);
    }
  }
});

test("a types list named like object keys keeps none of them and does not touch the prototypes", () => {
  const names = ["constructor", "__proto__", "toString", "hasOwnProperty", "valueOf", "prototype", "steel__proto__"];
  const result = acceptedFilters({ state: { ...bare, types: names }, total: 1, facets: {} });
  assert.deepStrictEqual(result.types, []);
  assert.equal(Object.getPrototypeOf(result.types), Array.prototype);
  assert.equal(({}).steel, undefined);
  assert.equal(({}).polluted, undefined);
});

test("a rarity named like an object key is left as given, with and without a facet that lists it", () => {
  for (const name of ["constructor", "__proto__", "toString"]) {
    for (const facets of [{}, { rarity: [{ value: name, count: 2 }] }]) {
      const result = acceptedFilters({ state: { ...bare, rarity: name }, total: 2, facets });
      assert.equal(result.rarity, name);
      assert.equal(result.changed, false);
    }
  }
});

test("a types list of ten thousand and of a hundred thousand members is read in linear time and keeps the valid ones once", () => {
  const members = Array.from({ length: 100000 }, (_, i) => (i % 3 === 0 ? ["Fire", "steel", "NORMAL"][(i / 3) % 3] : `Bogus${i}`));
  const started = performance.now();
  const result = acceptedFilters({ state: { ...bare, types: members }, total: 1, facets: {} });
  const spent = performance.now() - started;
  assert.deepStrictEqual(result.types, ["Fire", "Metal", "Colorless"]);
  assert.ok(spent < 1000, `took ${Math.round(spent)} ms`);
});

test("acceptedFilters leaves a frozen state alone and returns lists that are its own", () => {
  const frozen = deepFreeze({ ...bare, types: ["Steel", "Bogus"], rarity: "rare holo", set: " me55 ", series: " x " });
  const facets = deepFreeze({ rarity: [{ value: "Rare Holo", count: 3 }] });
  const first = acceptedFilters({ state: frozen, total: 5, facets });
  const second = acceptedFilters({ state: frozen, total: 5, facets });
  assert.deepStrictEqual(first, { types: ["Metal"], rarity: "Rare Holo", set: "me55", series: "x", changed: true });
  assert.ok(first.types !== second.types, "two answers share one list");
  assert.ok(first.types !== frozen.types);
  const same = acceptedFilters({ state: frozen, total: 5, facets });
  same.types.push("Water");
  assert.deepStrictEqual(acceptedFilters({ state: frozen, total: 5, facets }).types, ["Metal"]);
});

test("changed is true exactly when one of the four filters differs from the state", () => {
  const facets = { rarity: [{ value: "Rare Holo", count: 3 }, { value: "Common", count: 3 }] };
  const unchanged = [
    { types: ["Fire", "Water"] }, { rarity: "Rare Holo" }, { rarity: "bogus" }, { set: "me55" }, { series: "Mega Evolution" }, {},
  ];
  for (const fields of unchanged) assert.equal(acceptedFilters({ state: { ...bare, ...fields }, total: 5, facets }).changed, false, JSON.stringify(fields));
  const changed = [
    { types: ["Fire", "Bogus"] }, { types: ["fire"] }, { types: ["Fire", "Fire"] }, { types: [" Fire"] }, { rarity: "rare holo" }, { rarity: " Rare Holo" },
    { rarity: "  " }, { set: " me55" }, { series: "x " }, { set: "  " },
  ];
  for (const fields of changed) assert.equal(acceptedFilters({ state: { ...bare, ...fields }, total: 5, facets }).changed, true, JSON.stringify(fields));
});

// addressWithFilters

const none = { types: [], rarity: "", set: "", series: "" };

test("a rewritten address reads back, through the page's own reader, as the filters it was given", () => {
  const sets = ["me55", "base1", "swsh12pt5gg", "cel25c", "sv3pt5", "ex10", "pop2", "A&B", "x y", "x+y", "100%", "a=b", "#c", "é", "😀", "x,y"];
  const cases = [];
  for (const value of STORED_RARITIES) cases.push({ ...none, rarity: value });
  for (const value of sets) cases.push({ ...none, set: value, series: value });
  cases.push({ types: ["Metal", "Colorless", "Fire"], rarity: "Rare Holo", set: "me55", series: "Mega Evolution" });
  for (const filters of cases) {
    const noisy = "?q=%F0%9F%98%80+a%26b%3Dc%23d%25e&sort=hp&types=Bogus&rarity=junk&set=old&series=old";
    const next = addressWithFilters(noisy, filters);
    const read = readAddress(`?${next}`);
    assert.deepStrictEqual(read.types, filters.types, next);
    assert.equal(read.rarity, filters.rarity, next);
    assert.equal(read.set, filters.set, next);
    assert.equal(read.series, filters.series, next);
    assert.equal(read.q, "😀 a&b=c#d%e", next);
    assert.equal(read.sort, "hp", next);
  }
});

test("a filter that became empty is taken out of the address, never left as an empty value", () => {
  assert.equal(addressWithFilters("?types=Bogus&set=&rarity=&series=", none), "");
  assert.equal(addressWithFilters("types=Bogus&q=x", none), "q=x");
  assert.equal(addressWithFilters("", none), "");
  assert.equal(addressWithFilters("?", none), "");
});

test("fields the function does not own keep their place, count and meaning, whatever order the filters came in", () => {
  const before = "?zzz=1&series=old&q=a%20b&types=Fire&sort=name&order=asc&rarity=x&aaa&supertype=trainer&set=y&hp_min=3&q2=%2B";
  const after = addressWithFilters(before, { types: ["Fire"], rarity: "Rare Holo", set: "y", series: "old" });
  const names = (search) => [...new URLSearchParams(search).keys()];
  assert.deepStrictEqual(names(after), names(before));
  const wasKept = (search) => Object.fromEntries([...new URLSearchParams(search)].filter(([k]) => !["types", "set", "rarity", "series"].includes(k)));
  assert.deepStrictEqual(wasKept(after), wasKept(before));
  assert.equal(new URLSearchParams(after).get("rarity"), "Rare Holo");
});

test("a repeated filter field is written once, as the first value the page read", () => {
  const search = "?types=Fire&types=Water&rarity=a&rarity=b";
  const read = readAddress(search);
  assert.deepStrictEqual(read.types, ["Fire"]);
  const next = addressWithFilters(search, { types: ["Fire"], rarity: "a", set: "", series: "" });
  assert.deepStrictEqual(new URLSearchParams(next).getAll("types"), ["Fire"]);
  assert.deepStrictEqual(new URLSearchParams(next).getAll("rarity"), ["a"]);
});

test("addressWithFilters applied twice gives what it gave once", () => {
  const filters = { types: ["Metal"], rarity: "Rare Holo", set: "me55", series: "S & S" };
  const once = addressWithFilters("?q=x&types=steel", filters);
  assert.equal(addressWithFilters(`?${once}`, filters), once);
});

// rewriteFilterAddress

function stubHistory({ search, hash = "", marker = null }) {
  const writes = [];
  globalThis.location = { search, pathname: "/", hash };
  globalThis.history = {
    state: marker,
    replaceState(entry, _title, url) {
      writes.push({ entry, url });
      globalThis.history.state = entry;
      const at = url.indexOf("?");
      const cut = url.indexOf("#");
      globalThis.location.search = at < 0 ? "" : url.slice(at, cut < 0 ? undefined : cut);
    },
    pushState() { throw new Error("pushState: a rewrite added an entry"); },
  };
  return writes;
}

test("the rewrite replaces the entry once, keeps the marker and the hash, and a second call writes nothing", () => {
  const marker = { card: "base1-4" };
  const writes = stubHistory({ search: "?types=Fire,Bogus&q=x", hash: "#card=base1-4", marker });
  readStateFromURL();
  const filters = acceptedFilters({ state, total: 5, facets: {} });
  Object.assign(state, filters);
  rewriteFilterAddress();
  assert.equal(writes.length, 1);
  assert.equal(writes[0].url, "/?types=Fire&q=x#card=base1-4");
  assert.ok(writes[0].entry === marker, "the entry's state was replaced");
  rewriteFilterAddress();
  assert.equal(writes.length, 1, "an address that already holds the filters was written again");
});

test("the rewrite of an address holding nothing to change writes nothing, and a filter-free result leaves a bare path", () => {
  const writes = stubHistory({ search: "?types=Bogus", hash: "#stats" });
  readStateFromURL();
  Object.assign(state, acceptedFilters({ state, total: 20635, facets: {} }));
  rewriteFilterAddress();
  assert.equal(writes.length, 1);
  assert.equal(writes[0].url, "/#stats");
  const quiet = stubHistory({ search: "", hash: "" });
  readStateFromURL();
  rewriteFilterAddress();
  assert.equal(quiet.length, 0);
});

