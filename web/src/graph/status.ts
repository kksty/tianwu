// DOM tint for intent statuses.
//
// The canvas paints fixed dark-theme hues (render.ts CANVAS). The inspector and
// node panel sit on themed cards, so they must use the app's semantic tokens:
// the dark values match the canvas, and the light theme keeps enough contrast
// on a white card. Keeping the map here means the panel, the hover card and any
// future graph surface agree on one vocabulary.

/** Chip classes (border + tint + text) for one intent status. */
export const INTENT_STATUS_TINT: Record<string, string> = {
  // Pre-run / waiting family: no live work yet.
  pending: 'border-warning/20 bg-warning/12 text-warning',
  claimed: 'border-warning/20 bg-warning/12 text-warning',
  waiting: 'border-warning/20 bg-warning/12 text-warning',
  running: 'border-primary/25 bg-primary/12 text-primary',
  reviewing: 'border-review/25 bg-review/12 text-review',
  done: 'border-success/20 bg-success/12 text-success',
  failed: 'border-ai/25 bg-ai/12 text-ai',
  review_failed: 'border-caution/25 bg-caution/12 text-caution',
  parked: 'border-border bg-surface-2 text-ink-3',
}

/** The matching leading-dot class. */
export const INTENT_STATUS_DOT: Record<string, string> = {
  pending: 'bg-warning',
  claimed: 'bg-warning',
  waiting: 'bg-warning',
  running: 'bg-primary',
  reviewing: 'bg-review',
  done: 'bg-success',
  failed: 'bg-ai',
  review_failed: 'bg-caution',
  parked: 'bg-ink-3',
}

export function intentStatusTint(status?: string): string | undefined {
  return status ? INTENT_STATUS_TINT[status] : undefined
}

export function intentStatusDot(status?: string): string | undefined {
  return status ? INTENT_STATUS_DOT[status] : undefined
}
