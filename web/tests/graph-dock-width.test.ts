// The graph's right-hand overlays are resized by dragging their left edge. The
// arithmetic is tiny, but it is where a bad frame's width comes from: a stored
// number from a wider monitor must not push the dock over a narrow canvas, and
// the drag direction (the dock is anchored right, so dragging left widens it)
// is the kind of sign error that ships silently. These tests pin both.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  DOCK_DEFAULT_WIDTH,
  DOCK_MAX_WIDTH,
  DOCK_MIN_WIDTH,
  clampDockWidth,
  dockBounds,
  dockWidthFromDrag,
} from '../src/components/graph/dock-width.ts'

test('the dock leaves room for the map and never gets unreadably narrow', () => {
  // A wide window: the cap is the absolute maximum, not the viewport.
  assert.deepEqual(dockBounds(1920), { min: DOCK_MIN_WIDTH, max: DOCK_MAX_WIDTH })
  // A normal laptop window: the map keeps its share instead of the dock taking
  // whatever was stored on a bigger screen.
  assert.deepEqual(dockBounds(900), { min: DOCK_MIN_WIDTH, max: 900 - 24 - 320 })
  // Too small even for the minimum map share: the readable minimum wins, and the
  // overlay's CSS max-width keeps it on screen.
  assert.deepEqual(dockBounds(600), { min: DOCK_MIN_WIDTH, max: DOCK_MIN_WIDTH })
  // No viewport information (SSR/test render): fall back to the absolute cap.
  assert.deepEqual(dockBounds(0), { min: DOCK_MIN_WIDTH, max: DOCK_MAX_WIDTH })
})

test('a width is clamped into the bounds, garbage falls back to the default', () => {
  assert.equal(clampDockWidth(360, 1440), 360)
  assert.equal(clampDockWidth(120, 1440), DOCK_MIN_WIDTH)
  assert.equal(clampDockWidth(4000, 1440), DOCK_MAX_WIDTH)
  assert.equal(clampDockWidth(700, 900), 556)
  // A stored value that is not a number must not produce a zero-width dock.
  assert.equal(clampDockWidth(Number.NaN, 1440), DOCK_DEFAULT_WIDTH)
  assert.equal(clampDockWidth(Number.POSITIVE_INFINITY, 1440), DOCK_DEFAULT_WIDTH)
  assert.equal(clampDockWidth(360.4, 1440), 360)
})

test('dragging the left edge widens when the pointer moves left', () => {
  // The dock is anchored on the right: a leftward drag has to grow it.
  assert.equal(dockWidthFromDrag(360, 1000, 900, 1440), 460)
  // Dragging right narrows it, but a readable dock is the floor.
  assert.equal(dockWidthFromDrag(460, 1000, 1100, 1440), 360)
  assert.equal(dockWidthFromDrag(360, 1000, 1100, 1440), DOCK_MIN_WIDTH)
  // …and every drag stops at the same bounds as a stored width.
  assert.equal(dockWidthFromDrag(360, 1000, 1200, 1440), DOCK_MIN_WIDTH)
  assert.equal(dockWidthFromDrag(740, 1000, -1000, 1440), DOCK_MAX_WIDTH)
  assert.equal(dockWidthFromDrag(740, 1000, -1000, 900), 556)
})
