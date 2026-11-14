// The graph's right-hand overlays (the pinned node panel, the 下发 dock and the
// hover card) all cover the map from the right edge. One fixed width was a
// guess: 360px is too narrow to read a fact's description beside the graph and
// too wide for a quick peek, and the operator could not do anything about it.
// The width is now set by dragging the overlay's left edge, and shared by every
// overlay — they occupy the same slice of the canvas, so whichever is open uses
// the size the operator chose.
//
// Pure arithmetic only (no DOM, no React): the drag/pointer glue lives in
// DockResize.tsx and the tests exercise these functions directly.

/** Narrowest readable dock; below this the description wraps per word. */
export const DOCK_MIN_WIDTH = 300
/** Widest dock, regardless of how much room the viewport has. */
export const DOCK_MAX_WIDTH = 760
/** Width before the operator drags anything. */
export const DOCK_DEFAULT_WIDTH = 360
/** Keyboard resize step for the focusable handle. */
export const DOCK_KEY_STEP = 24
/** The dock is anchored 12px from the right edge and leaves 12px there. */
const DOCK_EDGE_GAP = 24
/** The map always keeps this much width to the left of the dock. */
const DOCK_MIN_MAP_WIDTH = 320
export const DOCK_STORAGE_KEY = 'tw.graph.dockWidth'

export interface DockBounds {
  min: number
  max: number
}

/**
 * The width range `viewport` can honestly show: a dock never takes the whole
 * canvas, and a tiny window still gets the readable minimum (the overlay's CSS
 * max-width then does the final clamp so nothing overflows off-screen).
 */
export function dockBounds(viewport: number): DockBounds {
  const room = viewport > 0 ? viewport - DOCK_EDGE_GAP - DOCK_MIN_MAP_WIDTH : DOCK_MAX_WIDTH
  return {
    min: DOCK_MIN_WIDTH,
    max: Math.min(DOCK_MAX_WIDTH, Math.max(DOCK_MIN_WIDTH, room)),
  }
}

/** Clamp a width the operator (or storage) asked for into `dockBounds`. */
export function clampDockWidth(width: number, viewport: number): number {
  const { min, max } = dockBounds(viewport)
  if (!Number.isFinite(width)) return Math.round(Math.min(max, Math.max(min, DOCK_DEFAULT_WIDTH)))
  return Math.round(Math.min(max, Math.max(min, width)))
}

/**
 * Width after dragging the dock's left edge from `startX` to `clientX`: the dock
 * is anchored on the right, so moving the edge left makes it wider.
 */
export function dockWidthFromDrag(
  startWidth: number,
  startX: number,
  clientX: number,
  viewport: number,
): number {
  return clampDockWidth(startWidth + (startX - clientX), viewport)
}
