import { useEffect, useMemo, useState, type MouseEvent } from 'react'
import { fmtInt, runStatusLabel } from '@/lib/format'
import type { FleetProject, FleetRun } from '@/lib/types'

const LANE_HEIGHT = 34
const BAR_TONE: Record<string, string> = {
  running: 'bg-primary/80 ring-1 ring-primary/50',
  done: 'bg-success/70 ring-1 ring-success/30',
  failed: 'bg-destructive/75 ring-1 ring-destructive/40',
  canceled: 'bg-muted-foreground/35 ring-1 ring-muted-foreground/25',
}

interface Tip {
  x: number
  y: number
  title: string
  lines: string[]
}

function clock(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}`
}

function span(ms: number): string {
  if (ms < 1000) return '0s'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`
}

/**
 * The 态势 page's big picture: one swimlane per project, each run a bar from its
 * start to its end (or to now while it is live). The bar length is the only
 * honest progress a run has — the ledger records no percentage, so none is
 * invented here. A marker on the lane is the commander's last round.
 */
export function FleetTimeline({
  projects,
  windowMinutes,
  onOpen,
}: {
  projects: FleetProject[]
  windowMinutes: number
  onOpen: (project: string, run?: string) => void
}) {
  const [now, setNow] = useState(() => Date.now())
  const [tip, setTip] = useState<Tip | null>(null)

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])

  const start = now - windowMinutes * 60_000
  const total = Math.max(1, now - start)
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => start + total * f)

  const lanes = useMemo(
    () =>
      projects.map((project) => {
        const bars = (project.recent_runs ?? [])
          .map((run) => {
            const s = new Date(run.started_at).getTime()
            const e = run.ended_at ? new Date(run.ended_at).getTime() : now
            return { run, s, e }
          })
          .filter((b) => Number.isFinite(b.s) && Number.isFinite(b.e) && b.e >= start && b.s <= now)
        return { project, bars }
      }),
    [projects, now, start],
  )

  const pct = (ms: number) => Math.max(0, Math.min(100, ((ms - start) / total) * 100))

  const showTip = (event: MouseEvent, run: FleetRun, s: number, e: number) => {
    const lines = [
      `${run.node || '—'} · ${runStatusLabel(run)}${run.purpose ? ` · ${run.purpose}` : ''}`,
      `开始 ${clock(s)} · 持续 ${span(Math.max(0, e - s))}`,
      `${fmtInt(run.tokens)} tok · ${fmtInt(run.events)} 事件`,
    ]
    if (run.declared_facts) lines.push(`声明事实 ${fmtInt(run.declared_facts)}`)
    if (run.stop_reason) lines.push(`停止：${run.stop_reason}`)
    setTip({
      x: Math.min(event.clientX + 14, window.innerWidth - 270),
      y: Math.min(event.clientY + 14, window.innerHeight - 150),
      title: run.title || '(无标题)',
      lines,
    })
  }

  if (projects.length === 0) {
    return (
      <div className="py-10 text-center text-[12px] text-muted-foreground">
        还没有项目。创建项目并派发意图后，这里会出现运行时间线。
      </div>
    )
  }

  return (
    <div className="relative">
      {/* time axis: same two columns as the lanes, so a label sits exactly on
          its gridline. The end labels hug the chart edges instead of hanging
          half outside it. */}
      <div className="mb-0.5 flex text-[10px] text-muted-foreground">
        <div className="w-28 shrink-0" aria-hidden />
        <div className="relative h-4 flex-1">
          {ticks.map((t, i) => {
            const first = i === 0
            const last = i === ticks.length - 1
            return (
              <span
                key={i}
                className={`absolute top-0 whitespace-nowrap leading-4 ${
                  first ? 'left-0' : last ? 'right-0' : ''
                }`}
                style={first || last ? undefined : { left: `${i * 25}%`, transform: 'translateX(-50%)' }}
              >
                {last ? `现在 ${clock(t)}` : clock(t)}
              </span>
            )
          })}
        </div>
      </div>

      <div className="min-h-[60px]">
        {lanes.map(({ project, bars }) => {
          const dec = project.decider
          const roundAt = dec?.last_round_at ? new Date(dec.last_round_at).getTime() : 0
          const roundPct = roundAt >= start && roundAt <= now ? pct(roundAt) : null
          return (
            <div key={project.id} className="flex items-stretch">
              <button
                type="button"
                onClick={() => onOpen(project.id)}
                className="flex w-28 shrink-0 items-center gap-1.5 pr-2 text-left hover:text-primary"
                title={`${project.name}${project.scope ? ` · ${project.scope}` : ''}`}
              >
                <span className="truncate text-[11px] text-foreground/85">{project.name}</span>
                {project.intents.running > 0 && (
                  <span className="ml-auto shrink-0 font-mono text-[10px] text-primary">
                    {project.intents.running}
                  </span>
                )}
              </button>

              <div className="relative flex-1 border-t border-border/30" style={{ height: LANE_HEIGHT }}>
                {[25, 50, 75].map((left) => (
                  <span
                    key={left}
                    className="pointer-events-none absolute bottom-0 top-0 w-px bg-border/25"
                    style={{ left: `${left}%` }}
                  />
                ))}
                <span className="pointer-events-none absolute bottom-0 right-0 top-0 w-px bg-border/40" />

                {bars.map(({ run, s, e }) => {
                  const from = pct(Math.max(s, start))
                  const to = pct(Math.min(e, now))
                  // Keep short bars visible without letting them spill past the
                  // right edge of the window.
                  const width = Math.min(Math.max(to - from, 0.6), Math.max(0, 100 - from))
                  return (
                    <button
                      key={run.id}
                      type="button"
                      onClick={() => onOpen(project.id, run.id)}
                      onMouseEnter={(event) => showTip(event, run, s, e)}
                      onMouseMove={(event) => showTip(event, run, s, e)}
                      onMouseLeave={() => setTip(null)}
                      className={`absolute top-1.5 h-[15px] rounded-sm transition-[filter] hover:brightness-125 ${
                        BAR_TONE[run.status] ?? 'bg-muted-foreground/40'
                      } ${run.status === 'running' ? 'animate-pulse' : ''}`}
                      style={{ left: `${from}%`, width: `${width}%` }}
                      aria-label={`${project.name} · ${run.title || run.id} · ${runStatusLabel(run)}`}
                    />
                  )
                })}

                {roundPct !== null && (
                  <span
                    className="pointer-events-none absolute top-0 h-full w-px bg-warning/60"
                    style={{ left: `${roundPct}%` }}
                  />
                )}
                {bars.length === 0 && (
                  <span className="pointer-events-none absolute inset-y-0 left-2 flex items-center text-[10px] text-muted-foreground/60">
                    这段时间没有运行
                  </span>
                )}
              </div>
            </div>
          )
        })}
      </div>

      {tip && (
        <div
          className="pointer-events-none fixed z-[60] w-[250px] rounded-md border border-border bg-popover/95 p-2 text-[11px] shadow-elevated backdrop-blur"
          style={{ left: tip.x, top: tip.y }}
        >
          <div className="truncate font-medium text-foreground" title={tip.title}>
            {tip.title}
          </div>
          {tip.lines.map((line, i) => (
            <div key={i} className="mt-0.5 text-muted-foreground">
              {line}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
