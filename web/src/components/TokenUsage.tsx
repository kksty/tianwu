// Project token total, compact one-line in the page header (docs/34 §3).
//
// It renders exactly what the ledger recorded from model-reported usage — no
// estimate, no budget, no zero-filling. The board aggregate (TokenTotals) is the
// caller; the type stays structural so the same line can render any usage row
// (TokenUsage) without dragging hour buckets back into the UI.
//
// The header only has room for the total: hover/focus opens the breakdown so the
// exact 入/出/缓存 figures stay one deliberate gesture away instead of widening
// the banner for every reader.

import { ChevronDown } from 'lucide-react'
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

const CAPSULE =
  'group relative inline-flex h-7 shrink-0 items-center gap-1 whitespace-nowrap rounded-md border border-border/60 bg-muted/20 px-2 font-mono text-[11px] tabular-nums text-muted-foreground'

function Figure({ label, value }: { label: string; value: string }) {
  return (
    <span className="whitespace-nowrap">
      {label}{' '}
      <span className="font-medium text-foreground">{value}</span>
    </span>
  )
}

export function ProjectTokens({ usage }: { usage?: ProjectTokenUsage }) {
  // No usage row, or a TokenUsage the provider never measured: the total is
  // unknown, and saying "0" would invent a measurement.
  const unknown = !usage || usage.known === false
  return (
    <span
      data-testid="project-tokens"
      role="group"
      tabIndex={0}
      aria-label={unknown ? '模型 token 用量未知' : '模型 token 用量，悬停查看明细'}
      title={titleOf(usage)}
      className={CAPSULE}
    >
      {unknown ? (
        <span>
          tokens <span className="font-semibold text-foreground">—</span>
        </span>
      ) : (
        <>
          <span>
            tokens{' '}
            <span className="text-[11px] font-semibold text-foreground">{num(usage.total_tokens)}</span>
          </span>
          <ChevronDown className="h-3 w-3 transition-transform group-hover:rotate-180 group-focus-within:rotate-180" />
          <span className="pointer-events-none absolute right-0 top-full z-30 mt-1 w-60 rounded-md border border-border bg-card p-2 text-[11px] opacity-0 shadow-elevated transition-opacity group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100">
            <span className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
              <Figure label="入" value={num(usage.input_tokens)} />
              <Figure label="出" value={num(usage.output_tokens)} />
              <Figure label="缓存" value={num(usage.cached_input_tokens)} />
              {usage.requests !== undefined && <Figure label="请求" value={num(usage.requests)} />}
            </span>
            <span className="mt-1.5 block border-t border-border/60 pt-1 text-[10px] leading-relaxed text-muted-foreground">
              只记录模型上报用量；缓存输入是输入的子集，已计入总数。
            </span>
          </span>
        </>
      )}
    </span>
  )
}
