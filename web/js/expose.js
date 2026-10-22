/** Attach named exports to globalThis for inline onclick handlers in index.html. */
export function exposeGlobals(bindings) {
  Object.assign(globalThis, bindings);
}
