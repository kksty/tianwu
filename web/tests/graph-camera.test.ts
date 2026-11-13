// Camera-rule tests for the console graph. The map used to fit itself to the
// canvas whenever the ledger was rebuilt (a poll every few seconds), which
// yanked the view away from the node the operator was reading. These tests pin
// the replacement rules: open at a fixed zoom centred on the layout, never
// scale to fit except through the explicit 适配画布 action, and bring back the
// operator's last pan/zoom after a reload or a visit to another panel.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  OPEN_ZOOM,
  cameraStorageKey,
  layoutBounds,
  openCamera,
  parseCamera,
  type NodeExtent,
} from '../src/graph/camera.ts'

function extent(x: number, y: number, halfW = 37, halfH = 37): NodeExtent {
  return { x, y, halfW, halfH }
}

test('openCamera keeps a fixed zoom instead of shrinking a large graph to fit the canvas', () => {
  const small = [extent(0, 0), extent(120, 60)]
  const huge = [extent(0, 0), extent(9000, 4000)]
  const a = openCamera(small, 800, 600)
  const b = openCamera(huge, 800, 600)
  assert.ok(a && b)
  assert.equal(a.k, OPEN_ZOOM)
  assert.equal(b.k, OPEN_ZOOM)
})

test('openCamera centres the drawn layout in the viewport', () => {
  const cam = openCamera([extent(100, 0), extent(300, 40)], 800, 600)
  assert.ok(cam)
  assert.equal(cam.k, OPEN_ZOOM)
  assert.equal(cam.x, 800 / 2 - 200 * OPEN_ZOOM)
  assert.equal(cam.y, 600 / 2 - 20 * OPEN_ZOOM)
})

test('openCamera waits until both a layout and a canvas exist', () => {
  assert.equal(openCamera([], 800, 600), null)
  assert.equal(openCamera([extent(0, 0)], 0, 600), null)
  assert.equal(openCamera([extent(0, 0)], 800, 0), null)
})

test('layoutBounds spans every drawn extent', () => {
  const bounds = layoutBounds([extent(100, 20, 10, 5), extent(-40, -60, 8, 12)])
  assert.ok(bounds)
  assert.equal(bounds.minX, -48)
  assert.equal(bounds.maxX, 110)
  assert.equal(bounds.minY, -72)
  assert.equal(bounds.maxY, 25)
  assert.equal(layoutBounds([]), null)
})

test('parseCamera accepts only a well-formed saved view', () => {
  assert.deepEqual(parseCamera('{"x":12,"y":-3,"k":1.5}'), { x: 12, y: -3, k: 1.5 })
  assert.equal(parseCamera(null), null)
  assert.equal(parseCamera(''), null)
  assert.equal(parseCamera('not json'), null)
  assert.equal(parseCamera('null'), null)
  assert.equal(parseCamera('{"x":1,"y":2}'), null)
  assert.equal(parseCamera('{"x":1,"y":2,"k":0}'), null)
  assert.equal(parseCamera('{"x":1,"y":2,"k":-2}'), null)
  assert.equal(parseCamera('{"x":"1","y":2,"k":1}'), null)
})

test('cameraStorageKey keeps one camera per project', () => {
  assert.notEqual(cameraStorageKey('proj-a'), cameraStorageKey('proj-b'))
  assert.match(cameraStorageKey('proj-a'), /proj-a/)
})
