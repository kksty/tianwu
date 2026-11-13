import type { Camera } from './render'

/**
 * The zoom the map opens at.
 *
 * The camera never scales itself to the canvas: the ledger is polled every few
 * seconds and a live run keeps adding facts, so fitting on every rebuild would
 * move the picture out from under whatever the operator was reading. 「适配画布」
 * in the toolbar (and double-clicking the canvas) is the explicit fit.
 */
export const OPEN_ZOOM = 1

/** A node's drawn footprint in graph coordinates: anchor + half extents, label
 *  margin included. The canvas derives these from the rendered shape, so this
 *  module only does framing arithmetic and stays free of renderer imports. */
export interface NodeExtent {
  x: number
  y: number
  halfW: number
  halfH: number
}

export interface Bounds {
  minX: number
  minY: number
  maxX: number
  maxY: number
}

/**
 * Where one project's last camera lives. The view belongs to the project, not
 * to this component instance: the graph page unmounts when the operator opens
 * another panel and a reload tears it down, so the camera has to outlive both.
 */
export function cameraStorageKey(projectID: string): string {
  return `tw.graph.camera:${projectID}`
}

/**
 * Parse a persisted camera. Anything malformed reads as "no saved view", so a
 * bad value can never wedge the map at an impossible transform.
 */
export function parseCamera(raw: string | null): Camera | null {
  if (!raw) return null
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch {
    return null
  }
  if (!value || typeof value !== 'object') return null
  const { x, y, k } = value as Record<string, unknown>
  if (typeof x !== 'number' || typeof y !== 'number' || typeof k !== 'number') return null
  if (!Number.isFinite(x) || !Number.isFinite(y) || !Number.isFinite(k) || k <= 0) return null
  return { x, y, k }
}

/** Extent of a layout in graph coordinates, label margin included. */
export function layoutBounds(extents: NodeExtent[]): Bounds | null {
  if (extents.length === 0) return null
  let minX = Infinity
  let minY = Infinity
  let maxX = -Infinity
  let maxY = -Infinity
  for (const e of extents) {
    minX = Math.min(minX, e.x - e.halfW)
    minY = Math.min(minY, e.y - e.halfH)
    maxX = Math.max(maxX, e.x + e.halfW)
    maxY = Math.max(maxY, e.y + e.halfH)
  }
  return { minX, minY, maxX, maxY }
}

/**
 * Opening camera: the layout's centre at the viewport centre, at a fixed zoom.
 * A large engagement therefore opens readable in its middle instead of being
 * shrunk until the whole canvas fits.
 */
export function openCamera(
  extents: NodeExtent[],
  width: number,
  height: number,
  zoom = OPEN_ZOOM,
): Camera | null {
  if (width <= 0 || height <= 0) return null
  const b = layoutBounds(extents)
  if (!b) return null
  const cx = (b.minX + b.maxX) / 2
  const cy = (b.minY + b.maxY) / 2
  return { k: zoom, x: width / 2 - cx * zoom, y: height / 2 - cy * zoom }
}
