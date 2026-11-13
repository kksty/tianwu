/**
 * Content equality for JSON-shaped ledger data.
 *
 * Polling hands back freshly parsed JSON, so reference identity always differs
 * even when nothing changed. Setting state with that fresh object re-renders the
 * whole view for no visible reason; comparing content lets the data layer drop
 * the update. This is intentionally not a general-purpose deep-equal: callers
 * pass values that came from `JSON.parse` (acyclic objects/arrays/primitives).
 * Values outside that shape (Date, Map, class instances) are compared by their
 * own enumerable keys, which is why they must not be passed here.
 */
export function deepEqual(a: unknown, b: unknown): boolean {
  // Catches identical references, equal primitives, null/undefined and +0/-0.
  if (a === b) return true
  // JSON has no NaN, but a derived numeric field can carry one; treating NaN as
  // equal to itself keeps "same content" stable instead of re-rendering forever.
  if (typeof a === 'number' && typeof b === 'number') {
    return Number.isNaN(a) && Number.isNaN(b)
  }
  // null and undefined are different states here: "not loaded yet" vs "loaded
  // as null". Only both being the same can be equal, and that was handled above.
  if (a === null || b === null || a === undefined || b === undefined) return false
  if (typeof a !== 'object' || typeof b !== 'object') return false

  const aIsArray = Array.isArray(a)
  if (aIsArray !== Array.isArray(b)) return false
  if (aIsArray) {
    const left = a as unknown[]
    const right = b as unknown[]
    // Order matters: graph/fact lists are ordered snapshots, not sets.
    if (left.length !== right.length) return false
    for (let i = 0; i < left.length; i++) {
      if (!deepEqual(left[i], right[i])) return false
    }
    return true
  }

  const left = a as Record<string, unknown>
  const right = b as Record<string, unknown>
  const leftKeys = Object.keys(left)
  const rightKeys = Object.keys(right)
  // A missing key is a different shape than a key holding undefined, so compare
  // the key sets first and only then the values.
  if (leftKeys.length !== rightKeys.length) return false
  for (const key of leftKeys) {
    if (!Object.prototype.hasOwnProperty.call(right, key)) return false
    if (!deepEqual(left[key], right[key])) return false
  }
  return true
}
