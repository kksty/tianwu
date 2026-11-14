// The graph page uses one intent-status vocabulary in two places: the canvas
// paints fixed dark hues, while the panel/hover chips use themed tokens. These
// tests keep the two DOM maps complete, so a new status cannot silently fall
// back to a generic chip or disappear.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  INTENT_STATUS_DOT,
  INTENT_STATUS_TINT,
  intentStatusDot,
  intentStatusTint,
} from '../src/graph/status.ts'

const STATUSES = [
  'pending',
  'claimed',
  'waiting',
  'running',
  'reviewing',
  'done',
  'failed',
  'review_failed',
  'parked',
] as const

test('every intent status has a themed chip and dot', () => {
  for (const status of STATUSES) {
    assert.ok(INTENT_STATUS_TINT[status], `${status} chip`)
    assert.ok(INTENT_STATUS_DOT[status], `${status} dot`)
  }
  assert.equal(intentStatusTint('pending'), INTENT_STATUS_TINT.pending)
  assert.equal(intentStatusDot('reviewing'), INTENT_STATUS_DOT.reviewing)
  assert.equal(intentStatusTint('verified'), undefined, 'fact statuses keep the shared chip')
})

test('the pre-run family shares amber; reviewing and failed do not', () => {
  assert.equal(INTENT_STATUS_DOT.pending, INTENT_STATUS_DOT.claimed)
  assert.equal(INTENT_STATUS_DOT.pending, INTENT_STATUS_DOT.waiting)
  assert.notEqual(INTENT_STATUS_DOT.reviewing, INTENT_STATUS_DOT.pending)
  assert.notEqual(INTENT_STATUS_DOT.reviewing, INTENT_STATUS_DOT.done)
  assert.notEqual(INTENT_STATUS_DOT.failed, INTENT_STATUS_DOT.review_failed)
})
