import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  claimReviewSummary,
  claimVersionLabel,
  claimVersionOf,
  reviewCoversCurrentClaim,
  reviewVersionLabel,
  reviewVersionOf,
  sortReviewsNewestFirst,
} from '../src/lib/claim.ts'
import type { Fact, Review } from '../src/lib/types.ts'

function fact(over: Partial<Fact> = {}): Fact {
  return {
    id: 'f_example',
    intent_id: 'in_example',
    kind: 'vuln',
    title: '示例断言',
    summary: '',
    confidence: 'observed',
    status: 'unverified',
    claim_version: 2,
    claim_hash: 'hash-v2',
    created_at: '2026-11-11T00:00:00Z',
    ...over,
  }
}

function review(over: Partial<Review> = {}): Review {
  return {
    id: 'rv_example',
    fact_id: 'f_example',
    verdict: 'pass',
    claim_version: 2,
    claim_hash: 'hash-v2',
    created_at: '2026-11-11T01:00:00Z',
    ...over,
  }
}

test('a pass for the current assertion reads as verified', () => {
  const summary = claimReviewSummary(fact({ status: 'verified' }), [review()])
  assert.equal(summary.state, 'verified')
  assert.equal(summary.version, 2)
  assert.equal(summary.currentReviews.length, 1)
  assert.equal(summary.assertionChangedAfterReview, false)
  assert.match(summary.detail, /v2/)
})

test('a proof of an older assertion is stale, not verified, after the claim changed', () => {
  const summary = claimReviewSummary(fact({ status: 'unverified', claim_version: 2 }), [
    review({ id: 'rv_old', claim_version: 1, claim_hash: 'hash-v1', created_at: '2026-11-10T01:00:00Z' }),
  ])
  assert.equal(summary.state, 'stale')
  assert.equal(summary.assertionChangedAfterReview, true)
  assert.equal(summary.proved?.version, 1)
  assert.equal(summary.label, '复核的是旧断言')
  assert.match(summary.detail, /v1/)
  assert.match(summary.detail, /v2/)
  // The current version must be visible next to the review's version, not
  // collapsed into a single "verified" badge.
  assert.equal(reviewVersionLabel(summary.latestReview!), 'v1')
  assert.equal(reviewCoversCurrentClaim(summary.latestReview!, fact({ claim_version: 2 })), false)
})

test('a re-review of the new assertion supersedes the older proof', () => {
  const summary = claimReviewSummary(fact({ status: 'verified', claim_version: 3 }), [
    review({ id: 'rv_new', claim_version: 3, claim_hash: 'hash-v3', created_at: '2026-11-12T01:00:00Z' }),
    review({ id: 'rv_old', claim_version: 1, claim_hash: 'hash-v1', created_at: '2026-11-10T01:00:00Z' }),
  ])
  assert.equal(summary.state, 'verified')
  assert.equal(summary.assertionChangedAfterReview, false)
  assert.equal(summary.currentReviews.length, 1)
  assert.equal(summary.proved?.version, 3)
})

test('review order is not trusted: the newest proof wins even if the list is oldest-first', () => {
  const sorted = sortReviewsNewestFirst([
    review({ id: 'rv_old', created_at: '2026-11-10T00:00:00Z', claim_version: 1, verdict: 'reject' }),
    review({ id: 'rv_new', created_at: '2026-11-12T00:00:00Z', claim_version: 2, verdict: 'pass' }),
  ])
  assert.deepEqual(sorted.map((r) => r.id), ['rv_new', 'rv_old'])
  const summary = claimReviewSummary(fact({ status: 'verified' }), sorted)
  assert.equal(summary.proved?.version, 2)
  assert.equal(summary.state, 'verified')
})

test('a rejected current assertion is reported as rejected, not as stale proof', () => {
  const summary = claimReviewSummary(fact({ status: 'rejected', claim_version: 2 }), [
    review({ id: 'rv_reject', verdict: 'reject' }),
  ])
  assert.equal(summary.state, 'rejected')
  assert.equal(summary.assertionChangedAfterReview, false)
  assert.match(summary.label, /否决/)
})

test('legacy reviews without a claim version are unbound, never implied to prove the current assertion', () => {
  const release: Review = {
    id: 'rv_legacy',
    fact_id: 'f_example',
    verdict: 'pass',
    created_at: '2026-11-10T00:00:00Z',
  }
  assert.equal(reviewVersionOf(release), 0)
  assert.equal(reviewVersionLabel(release), '版本未记录')
  const summary = claimReviewSummary(fact({ status: 'unverified', claim_version: 2 }), [release])
  assert.equal(summary.state, 'unbound')
  assert.equal(summary.assertionChangedAfterReview, false)
  assert.equal(summary.proved?.version, 0)
})

test('a fact with no reviews is unreviewed rather than silently verified', () => {
  const summary = claimReviewSummary(fact({ status: 'unverified' }), [])
  assert.equal(summary.state, 'unreviewed')
  assert.equal(summary.latestReview, undefined)
  assert.equal(summary.detail.length > 0, true)
})

test('claim version display never invents v1 for an unrecorded version', () => {
  assert.equal(claimVersionOf({ claim_version: 0 }), 0)
  assert.equal(claimVersionOf({ claim_version: 3 }), 3)
  assert.equal(claimVersionLabel(0), '版本未记录')
  assert.equal(claimVersionLabel(undefined), '版本未记录')
  assert.equal(claimVersionLabel(4), 'v4')
})

test('a needs_evidence verdict on the current assertion keeps it unverified', () => {
  const summary = claimReviewSummary(fact({ status: 'unverified', claim_version: 2 }), [
    review({ id: 'rv_ne', verdict: 'needs_evidence' }),
  ])
  assert.equal(summary.state, 'needs_evidence')
  assert.match(summary.label, /缺证据/)
})

test('reviews of older assertions without a proof read as changed, waiting for review', () => {
  const summary = claimReviewSummary(fact({ status: 'unverified', claim_version: 3 }), [
    review({ id: 'rv_ne', verdict: 'needs_evidence', claim_version: 1, created_at: '2026-11-10T00:00:00Z' }),
  ])
  assert.equal(summary.state, 'changed')
  assert.equal(summary.assertionChangedAfterReview, false)
  assert.match(summary.detail, /v3/)
})
