import { useMemo } from 'react'
import { BarChart3 } from 'lucide-react'
import { EmptyState } from '@cyber/ui'
import { fmtInt } from '@/lib/format'
import type { FleetProject } from '@/lib/types'

// A project's stack segment colour is a token, not a brand hue: the legend is
// read as "which project", while the height carries the only number that matters.
const COLORS = [
  'var(--primary)',
  'var(--info)',
  'var(--success)',
  'var(--warning)',
  'var(--ai)',
  'var(--caution)',
  'var(--ink-2)',
  'var(--ink-3)',
]

function colorFor(index: number): string {
  return COLORS[index % COLORS.length]
}

function clock(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}`
}

/**
 * Token spend history, attributed to the minute a run finished — that is when
 * the harness reports its total, and spreading it over the run's lifetime would
 * invent numbers the ledger never had. Sparse server points are expanded into
 * one bucket per minute so the bars line up with the timeline above.
 */
export function TokenHistory({
  projects,
  windowMinutes,
  nodeFilter = '',
}: {
  projects: FleetProject[]
  windowMinutes: number
  nodeFilter?: string
}) {
  const now = Date.now()
  const end = Math.floor(now / 60_000) * 60_000
  const start = end - (windowMinutes - 1) * 60_000

  const { buckets, total, runs } = useMemo(() => {
    type Segment = { id: string; name: string; tokens: number; color: string }
    const byMinute = new Map<number, { total: number; runs: number; segments: Segment[] }>()
    for (let i = 0; i < windowMinutes; i++) {
      byMinute.set(start + i * 60_000, { total: 0, runs: 0, segments: [] })
    }
    projects.forEach((project, index) => {
      const color = colorFor(index)
      for (const point of project.token_series ?? []) {
        if (nodeFilter && point.node !== nodeFilter) continue
        const at = Math.floor(new Date(point.at).getTime() / 60_000) * 60_000
        const bucket = byMinute.get(at)
        if (!bucket) continue
        bucket.total += point.tokens
        bucket.runs += point.runs ?? 0
        bucket.segments.push({ id: project.id, name: project.name, tokens: point.tokens, color })
      }
    })
    const list = [...byMinute.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([at, value]) => ({ at, ...value }))
    return {
      buckets: list,
      total: list.reduce((n, b) => n + b.total, 0),
      runs: list.reduce((n, b) => n + b.runs, 0),
    }
  }, [projects, start, windowMinutes, nodeFilter])

  const max = Math.max(1, ...buckets.map((b) => b.total))

  if (total === 0) {
    return (
      <EmptyState
        compact
        icon={BarChart3}
        title="这段时间没有 token 记录"
        description="token 在 run 结算时才记账；等 run 结束或换更长的时间窗口后，这里会出现堆叠柱。"
      />
    )
  }

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 text-[11px] text-ink-3">
        <span>按 run 结束时刻归集，堆叠 = 项目</span>
        <span className="tw-nums font-mono">
          {fmtInt(total)} tok · {fmtInt(runs)} run · 峰值 {fmtInt(max)}
        </span>
      </div>

      <div className="flex h-24 items-end gap-px border-b border-hairline" role="img" aria-label="token 消耗历史柱状图">
        {buckets.map((bucket) => (
          <div
            key={bucket.at}
            className="group relative flex h-full flex-1 items-end"
            title={`${clock(bucket.at)} · ${fmtInt(bucket.total)} tok · ${bucket.runs} run`}
          >
            <div
              className="flex w-full flex-col-reverse overflow-hidden rounded-t-[1px] transition-opacity group-hover:opacity-80"
              style={{ height: `${Math.max((bucket.total / max) * 100, bucket.total > 0 ? 2 : 0)}%` }}
            >
              {bucket.segments.map((segment) => (
                <div
                  key={segment.id}
                  style={{
                    height: `${(segment.tokens / Math.max(bucket.total, 1)) * 100}%`,
                    background: segment.color,
                  }}
                  title={`${segment.name} · ${fmtInt(segment.tokens)} tok`}
                />
              ))}
            </div>
          </div>
        ))}
      </div>

      <div className="flex items-center justify-between font-mono text-[11px] text-ink-3">
        <span className="tw-nums">{clock(start)}</span>
        <span className="tw-nums">{clock(end)}</span>
      </div>

      <div className="flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-ink-3">
        {projects.map((project, index) => (
          <span key={project.id} className="flex min-w-0 items-center gap-1">
            <span className="h-2 w-2 shrink-0 rounded-sm" style={{ background: colorFor(index) }} />
            <span className="max-w-[120px] truncate" title={project.name}>
              {project.name}
            </span>
          </span>
        ))}
      </div>
    </div>
  )
}
