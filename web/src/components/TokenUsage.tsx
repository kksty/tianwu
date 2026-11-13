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
//
// One surface only: the caveats live inside this same panel. A native `title`
// on the trigger used to pop a grey browser tooltip on top of the open panel
// after a second of hovering — two overlays for one control. Do not reintroduce
// `title` here; extend the panel instead.

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

const CAPSULE =
  'group relative inline-flex h-6 shrink-0 items-center gap-1 whitespace-nowrap rounded-sm font-mono text-[11px] text-ink-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-offset-2 focus-visible:ring-offset-background'

/** One figure row in the panel: a quiet label and a tabular number. */
function Figure({ label, value }: { label: string; value: string }) {
  return (
    <>
      <span className="text-ink-3">{label}</span>
      <span className="tw-nums text-right font-medium text-ink-2">{value}</span>
    </>
  )
}

export function ProjectTokens({ usage }: { usage?: ProjectTokenUsage }) {
  // No usage row, or a TokenUsage the provider never measured: the total is
  // unknown, and saying "0" would invent a measurement.
  const unknown = !usage || usage.known === false
  const missing = usage?.missing_usage ?? usage?.missing ?? 0
  const unknownRuns = usage?.unknown_usage_runs ?? 0
  return (
    <span
      data-testid="project-tokens"
      role="group"
      tabIndex={0}
      aria-label={unknown ? '模型 token 用量未知，悬停查看说明' : '模型 token 用量，悬停查看明细'}
      className={CAPSULE}
    >
      {unknown ? (
        <span>
          tokens <span className="font-semibold text-ink-2">—</span>
        </span>
      ) : (
        <span>
          tokens <span className="tw-nums text-[11px] font-semibold text-ink">{num(usage.total_tokens)}</span>
        </span>
      )}
      <ChevronDown className="h-3.5 w-3.5 text-ink-3 transition-transform group-hover:rotate-180 group-focus-within:rotate-180" />
      {/* Flush against the trigger (no margin) so the pointer can travel from
          the number into the panel without crossing a dead gap that would
          close it and make the hover feel broken. */}
      <span className="pointer-events-none absolute right-0 top-full z-30 w-60 rounded-lg border border-hairline bg-popover p-2.5 text-[11px] opacity-0 shadow-elevated transition-opacity group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100">
        {unknown ? (
          <span className="block leading-relaxed text-ink-3">
            模型没有上报用量，所以这里不显示数字：不估算、不补零。
          </span>
        ) : (
          <>
            <span className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
              <Figure label="入" value={num(usage.input_tokens)} />
              <Figure label="出" value={num(usage.output_tokens)} />
              <Figure label="缓存" value={num(usage.cached_input_tokens)} />
              {usage.requests !== undefined && <Figure label="请求" value={num(usage.requests)} />}
            </span>
            <span className="mt-2 block border-t border-hairline pt-1.5 leading-relaxed text-ink-3">
              只记录模型上报用量；缓存输入是输入的子集，已计入总数。
              {missing > 0 && <span className="mt-1 block">其中 {fmtInt(missing)} 次请求未上报用量。</span>}
              {unknownRuns > 0 && <span className="mt-1 block">{fmtInt(unknownRuns)} 个 run 的用量未知。</span>}
            </span>
          </>
        )}
      </span>
    </span>
  )
}
