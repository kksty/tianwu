import { useMemo } from 'react'
import { fmtInt } from '@/lib/format'
import type { FleetProject } from '@/lib/types'

const COLORS = ['#ff4d4d', '#6aa9ff', '#39d98a', '#ffc457', '#b388ff', '#38bdf8', '#ff9f43', '#f2c94c']

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
      <div className="py-6 text-center text-[12px] text-muted-foreground">
        这段时间没有已结束 run 的 token 记录（token 在 run 结束时才记账）。
      </div>
    )
  }

  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between text-[10px] text-muted-foreground">
        <span>按 run 结束时刻归集，堆叠 = 项目</span>
        <span className="font-mono">
          {fmtInt(total)} tok · {fmtInt(runs)} run · 峰值 {fmtInt(max)}
        </span>
      </div>

      <div className="flex h-24 items-end gap-px border-b border-border/50">
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

      <div className="flex items-center justify-between text-[10px] text-muted-foreground">
        <span>{clock(start)}</span>
        <span>{clock(end)}</span>
      </div>

      <div className="flex flex-wrap gap-x-3 gap-y-1 text-[10px] text-muted-foreground">
        {projects.map((project, index) => (
          <span key={project.id} className="flex items-center gap-1">
            <span className="h-2 w-2 rounded-sm" style={{ background: colorFor(index) }} />
            <span className="max-w-[120px] truncate" title={project.name}>
              {project.name}
            </span>
          </span>
        ))}
      </div>
    </div>
  )
}
