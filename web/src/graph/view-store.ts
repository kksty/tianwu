// The canvas owns two pieces of view state that outlive a single paint: what the
// pointer is over, and where the camera is. Neither belongs in GraphView's React
// state — a hover change would re-render the whole page (inspector, panels,
// toolbar) for a highlight that the canvas can draw in its own frame, and the
// camera changes on every animation frame while panning. So the canvas publishes
// them here, and only the components that actually display them (the hover
// inspector, the minimap) subscribe.
//
// Both stores are deliberately tiny: a value, a listener set, and a
// `useSyncExternalStore` hook. Values are compared before notifying, so a
// subscriber never re-renders for an unchanged view; the camera object is copied
// on publish because the canvas keeps mutating its own ref in place.
import { useSyncExternalStore } from 'react'
import type { Camera } from './render'

export interface GraphView {
  camera: Camera
  width: number
  height: number
}

const EMPTY_VIEW: GraphView = { camera: { x: 0, y: 0, k: 1 }, width: 0, height: 0 }

let hovered: string | null = null
let view: GraphView = EMPTY_VIEW

const hoverListeners = new Set<() => void>()
const viewListeners = new Set<() => void>()

function notify(listeners: Set<() => void>) {
  // Copy: a listener may unsubscribe while we are iterating.
  for (const listener of [...listeners]) listener()
}

function subscribeHover(listener: () => void): () => void {
  hoverListeners.add(listener)
  return () => {
    hoverListeners.delete(listener)
  }
}

function subscribeView(listener: () => void): () => void {
  viewListeners.add(listener)
  return () => {
    viewListeners.delete(listener)
  }
}

const readHovered = () => hovered
const readView = () => view

/** Publish the hovered node/edge id (null = nothing). No-op when unchanged. */
export function setHovered(id: string | null): void {
  if (id === hovered) return
  hovered = id
  notify(hoverListeners)
}

/** The hovered node/edge id, or null. Re-renders only this component's consumers. */
export function useHovered(): string | null {
  return useSyncExternalStore(subscribeHover, readHovered, readHovered)
}

function sameView(a: GraphView, b: GraphView): boolean {
  return (
    a.width === b.width &&
    a.height === b.height &&
    a.camera.x === b.camera.x &&
    a.camera.y === b.camera.y &&
    a.camera.k === b.camera.k
  )
}

/**
 * Publish the camera and canvas size (the minimap's viewport rectangle). The
 * canvas coalesces this to one call per animation frame. No-op when the value did
 * not change, and the camera is copied so later in-place mutations of the
 * canvas's own camera cannot change a snapshot a subscriber already rendered.
 */
export function setView(next: GraphView): void {
  const snapshot: GraphView = {
    camera: { x: next.camera.x, y: next.camera.y, k: next.camera.k },
    width: next.width,
    height: next.height,
  }
  if (sameView(view, snapshot)) return
  view = snapshot
  notify(viewListeners)
}

/** The current camera + canvas size. Re-renders only this component's consumers. */
export function useView(): GraphView {
  return useSyncExternalStore(subscribeView, readView, readView)
}

/** Coalesce publishes to at most one per animation frame (latest value wins). */
export interface FramePublisher<T> {
  /** Queue a value; the first call in a frame schedules the publish. */
  publish(value: T): void
  /** Drop a pending publish (canvas unmounted, pointer left). */
  cancel(): void
  /** Publish the pending value now (teardown/tests). */
  flush(): void
}

/**
 * The one-frame merge rule the canvas publishes its camera with: a pan fires
 * many pointermoves per frame, but the minimap only has to see the last one.
 * The scheduler is injectable so the rule is testable without a DOM, the same
 * way `createMoveCoalescer` is (this lives beside the store it publishes to).
 */
export function createFramePublisher<T>(
  publish: (value: T) => void,
  schedule: (cb: () => void) => number = (cb) => requestAnimationFrame(cb),
  cancelFrame: (handle: number) => void = (handle) => cancelAnimationFrame(handle),
): FramePublisher<T> {
  let frame = 0
  let pending: { value: T } | null = null
  const run = () => {
    frame = 0
    const at = pending
    pending = null
    // A cancel() after the frame was queued leaves nothing to publish.
    if (at) publish(at.value)
  }
  return {
    publish(value) {
      pending = { value }
      if (frame === 0) frame = schedule(run)
    },
    cancel() {
      if (frame !== 0) cancelFrame(frame)
      frame = 0
      pending = null
    },
    flush() {
      if (frame !== 0) cancelFrame(frame)
      frame = 0
      const at = pending
      pending = null
      if (at) publish(at.value)
    },
  }
}
