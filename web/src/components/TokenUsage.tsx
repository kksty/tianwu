// Project token total, one line for a page header (docs/34 §3).
//
// It renders exactly what the ledger recorded from model-reported usage — no
// estimate, no budget, no zero-filling. The board aggregate (TokenTotals) is the
// caller; the type stays structural so the same line can render any usage row
// (TokenUsage) without dragging hour buckets back into the UI.

import { fmtInt } from '@/lib/format'

/** The fields the line can honestly show. Both `TokenTotals` (board.usage) and
 *  `TokenUsage` (per-intent) fit; absent numbers stay unknown ("—"). */
export interface ProjectTokenUsage {
  total_tokens?: number
  input_tokens?: number
  output_tokens?: number
  cached_input_tokens?: number
  requests?: number
  /** TokenUsage names unmeasured requests `missing`; TokenTotals `missing_usage`. */
  missing?: number
  missing_usage?: number
  unknown_usage_runs?: number
  /** TokenUsage only: false means the provider reported no measured usage. */
  known?: boolean
}

function num(n?: number): string {
  return n === undefined || n === null ? '—' : fmtInt(n)
}

/** Title: the caveats a reader needs before trusting the number. */
function titleOf(usage?: ProjectTokenUsage): string {
  return [
    '只记录模型上报的 token 用量，不设预算',
    '缓存输入是输入 token 的子集，已计入总数',
    `模型请求 ${num(usage?.requests)}`,
    `缺失用量的请求 ${num(usage?.missing_usage ?? usage?.missing)}`,
    `用量未知的 run ${num(usage?.unknown_usage_runs)}`,
  ].join('；')
}

export function ProjectTokens({ usage }: { usage?: ProjectTokenUsage }) {
  // No usage row, or a TokenUsage the provider never measured: the total is
  // unknown, and saying "0" would invent a measurement.
  if (!usage || usage.known === false) {
    return (
      <span
        data-testid="project-tokens"
        title={titleOf(usage)}
        className="whitespace-nowrap font-mono text-[11px] text-muted-foreground"
      >
        tokens —
      </span>
    )
  }
  return (
    <span
      data-testid="project-tokens"
      title={titleOf(usage)}
      className="whitespace-nowrap font-mono text-[11px] text-muted-foreground"
    >
      tokens {num(usage.total_tokens)}
      <span className="text-muted-foreground/50"> · </span>
      入 {num(usage.input_tokens)}
      <span className="text-muted-foreground/50"> / </span>
      出 {num(usage.output_tokens)}
      <span className="text-muted-foreground/50"> / </span>
      缓存 {num(usage.cached_input_tokens)}
    </span>
  )
}
