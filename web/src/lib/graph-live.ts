// Live-graph decision rules, kept pure so `node --test` can cover them without a
// renderer. The graph page is a fixed snapshot; "实时" only decides *when* a new
// whole snapshot is admitted, never how pages are merged.
//
// The probe asks the ledger whether any *content* row changed after the snapshot
// (GET /api/changes?since=<seq>). Run/message bookkeeping advances the durable
// seq without a versions row, so the raw watermark alone would wake the graph
// for writes that never touched a node.
//
// One reload may deliver several changes, so the minimum interval keeps a busy
// run from re-fetching the batch every poll; the small settle delay lets a burst
// of writes land in one snapshot instead of racing them one by one.

/** The part of a ledger version the graph compares. */
export interface GraphVersion {
  epoch: string
  seq: number
}

/** The minimum spacing between two automatic reloads. */
export const LIVE_RELOAD_MIN_MS = 3000
/** A short settle window so a burst of writes becomes one snapshot. */
export const LIVE_RELOAD_DELAY_MS = 800

/**
 * Whether the ledger has content the snapshot does not show yet.
 *
 * `pending` is the number of versions rows after the snapshot's seq (the
 * content journal, not the durable seq). Bookkeeping alone must not count: it
 * moves the watermark but leaves the versions table untouched. A different
 * ledger epoch always counts, because seq numbers no longer mean the same rows.
 */
export function hasNewContent(
  snapshot?: GraphVersion,
  watermark?: GraphVersion,
  pending = 0,
): boolean {
  if (!snapshot || !watermark || !watermark.epoch) return false
  if (watermark.epoch !== snapshot.epoch) return true
  return watermark.seq > snapshot.seq && pending > 0
}

/**
 * Whether the page should fetch a new snapshot now.
 *
 * Hidden tabs, a request already in flight, and an operator who turned 实时 off
 * all say no; a probe that found no new content has nothing to admit.
 */
export function shouldLiveReload(input: {
  live: boolean
  hidden: boolean
  loading: boolean
  busy: boolean
  hasNewChanges: boolean
}): boolean {
  return input.live && !input.hidden && !input.loading && !input.busy && input.hasNewChanges
}

/**
 * Delay before the reload fires. The first change after a quiet period waits
 * only the settle window; changes that keep arriving are throttled to
 * `minInterval` so pagination and layout work stay bounded.
 */
export function liveReloadDelay(
  now: number,
  lastReloadAt: number,
  minInterval = LIVE_RELOAD_MIN_MS,
  settle = LIVE_RELOAD_DELAY_MS,
): number {
  return Math.max(settle, minInterval - (now - lastReloadAt))
}
