// Assertion-version helpers for fact detail.
//
// A fact is not one timeless statement: store.UpdateFact fingerprints the claim
// (title/doc/evidence/…) and starts a new claim_version whenever it changes;
// store.ReviewFact binds a verdict to the version the verifier actually proved.
// The console therefore has to answer three different questions, and never blur
// them into one "verified" badge:
//   - what is the current assertion version (claim_version)?
//   - which version did each review prove (review.claim_version)?
//   - did the assertion change after the proof (a pass for vN while current is vM)?
//
// Everything here is a pure function over the rows the API already returns: we
// derive the "old proof no longer covers the current assertion" state from
// reviews[] + fact.claim_version rather than inventing a backend flag.
import type { Fact, Review } from './types.ts'

/** 0 means "the server did not record a version" (displayed honestly, not as v1). */
export function claimVersionOf(fact: Pick<Fact, 'claim_version'>): number {
  const version = fact.claim_version ?? 0
  return Number.isFinite(version) && version > 0 ? version : 0
}

/** Version label for a fact's *current* assertion. */
export function claimVersionLabel(version?: number): string {
  return version && version > 0 ? `v${version}` : '版本未记录'
}

/** 0 means a review row predates claim-bound verifications (legacy row). */
export function reviewVersionOf(review: Pick<Review, 'claim_version'>): number {
  const version = review.claim_version ?? 0
  return Number.isFinite(version) && version > 0 ? version : 0
}

/** Version label for one review's binding. */
export function reviewVersionLabel(review: Pick<Review, 'claim_version'>): string {
  const version = reviewVersionOf(review)
  return version > 0 ? `v${version}` : '版本未记录'
}

/** Does this review prove the assertion version the fact currently has? */
export function reviewCoversCurrentClaim(
  review: Pick<Review, 'claim_version'>,
  fact: Pick<Fact, 'claim_version'>,
): boolean {
  const current = claimVersionOf(fact)
  return current > 0 && reviewVersionOf(review) === current
}

/**
 * The state a reader should take away from fact + its review list.
 *
 * `stale` is the important one: at least one review proved an *older* assertion
 * and the current assertion has no verdict, so the old proof must not read as
 * "this finding is verified".
 */
export type ClaimState =
  | 'unreviewed'
  | 'verified'
  | 'rejected'
  | 'needs_evidence'
  | 'stale'
  | 'changed'
  | 'unbound'

export interface ClaimReviewSummary {
  /** Current assertion version (0 = not recorded). */
  version: number
  /** Reviews bound to the current version, newest first. */
  currentReviews: Review[]
  /** Newest review overall, which may belong to an older assertion. */
  latestReview?: Review
  /** Newest "pass" review: the version that was actually proven, when any. */
  proved?: { version: number; review: Review }
  /** A proof exists, but only for an older assertion version. */
  assertionChangedAfterReview: boolean
  state: ClaimState
  /** Short badge-sized status text. */
  label: string
  /** One sentence explaining the situation; '' when there is nothing to explain. */
  detail: string
}

function createdAt(review: Review): string {
  return review.created_at ?? ''
}

/** Newest first; the API already sorts this way, but the state must not depend on it. */
export function sortReviewsNewestFirst(reviews: Review[]): Review[] {
  return [...reviews].sort((a, b) => createdAt(b).localeCompare(createdAt(a)))
}

export function claimReviewSummary(
  fact: Pick<Fact, 'claim_version' | 'status'>,
  reviews: Review[] = [],
): ClaimReviewSummary {
  const version = claimVersionOf(fact)
  const sorted = sortReviewsNewestFirst(reviews)
  const currentReviews = version > 0 ? sorted.filter((r) => reviewVersionOf(r) === version) : []
  const latestReview = sorted[0]
  const newestPass = sorted.find((r) => r.verdict === 'pass')
  const proved = newestPass
    ? { version: reviewVersionOf(newestPass), review: newestPass }
    : undefined
  // A proof for an older assertion only counts as "changed after review" when
  // the current assertion has no verdict of its own; a re-review of the new
  // version supersedes the old note. A legacy review with no recorded version
  // cannot be claimed to have proven an older version — it stays "unbound".
  const assertionChangedAfterReview =
    Boolean(proved) &&
    version > 0 &&
    proved!.version > 0 &&
    proved!.version < version &&
    currentReviews.length === 0
  const current = currentReviews[0]

  let state: ClaimState
  if (fact.status === 'verified' && currentReviews.some((r) => r.verdict === 'pass')) {
    state = 'verified'
  } else if (fact.status === 'rejected' && current?.verdict === 'reject') {
    state = 'rejected'
  } else if (assertionChangedAfterReview) {
    state = 'stale'
  } else if (current?.verdict === 'reject') {
    state = 'rejected'
  } else if (current?.verdict === 'needs_evidence') {
    state = 'needs_evidence'
  } else if (current?.verdict === 'pass') {
    state = 'verified'
  } else if (reviews.length === 0) {
    state = 'unreviewed'
  } else if (sorted.every((r) => reviewVersionOf(r) === 0)) {
    // Legacy review rows carry no claim version at all: say so instead of
    // pretending they proved the current assertion.
    state = 'unbound'
  } else {
    state = 'changed'
  }

  const label: Record<ClaimState, string> = {
    unreviewed: '未复核',
    verified: '当前断言已复核通过',
    rejected: '当前断言已否决',
    needs_evidence: '当前断言缺证据',
    stale: '复核的是旧断言',
    changed: '断言已变更，待复核',
    unbound: '旧复核未绑定断言版本',
  }

  let detail = ''
  if (state === 'stale' && proved) {
    detail = `复核通过的是 ${claimVersionLabel(proved.version)} 的断言；当前断言已是 ${claimVersionLabel(version)} 且未复核，旧结论不能证明新内容。`
  } else if (state === 'changed') {
    detail = `有 ${sorted.length} 条复核记录，但没有针对当前 ${claimVersionLabel(version)} 的结论。`
  } else if (state === 'unbound') {
    detail = '这些复核记录没有记录所证明的断言版本，无法确认它们是否覆盖当前断言。'
  } else if (state === 'unreviewed') {
    detail = '还没有复核记录：这条事实只是入账观察，不是已验证结论。'
  } else if (state === 'verified') {
    detail =
      fact.status === 'verified'
        ? `${claimVersionLabel(version)} 由复核记录证明；断言内容再变会回到未复核。`
        : `当前 ${claimVersionLabel(version)} 有通过记录，但事实状态仍未复核（以账本状态为准）。`
  } else if (state === 'rejected') {
    detail = `否决记录针对当前 ${claimVersionLabel(version)}；重新打开需要新的断言版本或人工撤销。`
  } else if (state === 'needs_evidence') {
    detail = `复核判定当前 ${claimVersionLabel(version)} 缺证据，事实保持未复核。`
  }

  return {
    version,
    currentReviews,
    latestReview,
    proved,
    assertionChangedAfterReview,
    state,
    label: label[state],
    detail,
  }
}
