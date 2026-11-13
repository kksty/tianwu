import { useEffect, useState } from 'react'
import { Bot, ChevronDown, ChevronUp } from 'lucide-react'
import { Meter } from '@cyber/ui'
import { isPhaseActive, PHASE_LABEL, phaseDetail, phaseOf, phaseProgress } from '@/lib/decider'
import { fmtInt, fmtUptime } from '@/lib/format'
import type { BoardResponse } from '@/lib/types'

/**
 * The commander's live card: what the decider is doing right now, how long it
 * has been in that phase, and which directions are running. It reads the same
 * board the graph polls — no extra request — and the phase comes from the
 * decider itself (internal/decider.Activity), so it is a report, not a guess
 * reconstructed from the decision log.
 */
/** One-second clock, only while something is actually moving. */
function useTick(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [active])
  return now
}

export function CommanderCard({ board, onOpen }: { board?: BoardResponse; onOpen: () => void }) {
  const [open, setOpen] = useState(true)
  const dec = board?.decider
  const activity = dec?.activity
  const running = board?.running_intents ?? []

  const phase = phaseOf(dec)
  const active = isPhaseActive(phase)
  const now = useTick(active)

  const since = activity?.since ? new Date(activity.since).getTime() : 0
  const elapsed = since > 0 ? Math.max(0, Math.round((now - since) / 1000)) : 0
  const bar = active ? phaseProgress(elapsed) : 0
  const detail = phaseDetail(dec)

  const dot = active
    ? 'animate-pulse bg-info'
    : phase === 'paused'
      ? 'bg-warning'
      : phase === 'blocked'
        ? 'bg-caution'
        : 'bg-muted-foreground'

  return (
    <div className="pointer-events-auto w-[292px] overflow-hidden rounded-lg border border-border bg-card/90 shadow-lifted backdrop-blur">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left hover:bg-accent/40"
        title={open ? '收起指挥官状态' : '展开指挥官状态'}
      >
        <Bot className="h-3.5 w-3.5 shrink-0 text-ai" />
        <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">指挥官</span>
        <span className="ml-auto font-mono text-[10px] text-muted-foreground">
          {dec?.rounds ? `第 ${fmtInt(dec.rounds)} 轮` : '待命'}
        </span>
        {open ? (
          <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronUp className="h-3 w-3 shrink-0 text-muted-foreground" />
        )}
      </button>

      {open && (
        <div className="space-y-1.5 border-t border-border/70 px-2.5 py-1.5">
          <div className="flex items-center gap-1.5">
            <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dot}`} />
            <span className="text-[12px] font-medium text-foreground">{PHASE_LABEL[phase] ?? phase}</span>
            {active && since > 0 && (
              <span className="font-mono text-[10px] text-muted-foreground">{elapsed}s</span>
            )}
            {!active && dec?.uptime_seconds ? (
              <span className="ml-auto font-mono text-[10px] text-muted-foreground">
                已跑 {fmtUptime(dec.uptime_seconds)}
              </span>
            ) : null}
          </div>

          <Meter
            value={bar}
            max={100}
            tone={phase === 'blocked' || phase === 'paused' ? 'warning' : 'info'}
            className="h-1"
            aria-label="指挥官本轮进度（不会到 100：轮次结束由下一状态重置）"
          />

          <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
            {activity?.wake && (
              <span className="shrink-0 rounded bg-muted px-1 font-mono text-[10px]">{activity.wake}</span>
            )}
            <span className="min-w-0 flex-1 truncate" title={detail}>
              {detail}
            </span>
          </div>

          {running.length > 0 && (
            <div className="space-y-1 border-t border-border/70 pt-1.5">
              <div className="flex items-center justify-between text-[10px] uppercase tracking-wider text-muted-foreground">
                <span>在跑 {running.length}</span>
                {dec?.model && <span className="normal-case tracking-normal">{dec.model}</span>}
              </div>
              {running.slice(0, 3).map((item) => {
                const idle = item.pulse?.idle_seconds ?? 0
                const events = item.pulse?.events ?? 0
                return (
                  <div key={item.id} className="space-y-0.5">
                    <div className="flex items-center gap-1.5 text-[11px]">
                      <span
                        className={`h-1 w-1 shrink-0 rounded-full ${idle > 180 ? 'bg-warning' : 'bg-success'}`}
                      />
                      <span className="truncate text-foreground/90" title={item.title}>
                        {item.title}
                      </span>
                      <span className="ml-auto shrink-0 font-mono text-[10px] text-muted-foreground">
                        静默 {idle}s · {events} 事件
                      </span>
                    </div>
                    <Meter
                      value={Math.min(idle, 300)}
                      max={300}
                      tone={idle > 180 ? 'warning' : 'success'}
                      className="h-0.5"
                      aria-label={`${item.title} 的静默进度`}
                    />
                  </div>
                )
              })}
            </div>
          )}

          {/* Single-hour run tokens are retired (docs/34 §3): the project total
              lives on the graph page header; this card keeps the decider's own
              measured spend and the way to its log. */}
          <div className="flex items-center gap-1.5 border-t border-border/70 pt-1 text-[10px] text-muted-foreground">
            <span>决策 {fmtInt(dec?.tokens_total)} tok</span>
            <button type="button" className="ml-auto text-primary hover:underline" onClick={onOpen}>
              决策 →
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
