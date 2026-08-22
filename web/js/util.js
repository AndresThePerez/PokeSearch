// Two DOM helpers, shared by every module. This file imports nothing, so it
// can never take part in an import cycle.

export const $ = (id) => document.getElementById(id);

// element builds a node with an optional class and text. Text goes in as
// textContent, never markup — the whole frontend is innerHTML-free on purpose.
export function element(tag, className, textValue) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (textValue !== undefined) node.textContent = textValue;
  return node;
}
