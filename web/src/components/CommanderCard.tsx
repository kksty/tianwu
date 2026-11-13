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
 *
 * Visually it is one quiet card in the map's bottom-left stack: a card header
 * (icon + name + round), a phase line, a thin progress meter and up to three
 * running directions. Everything is 11–12px; no 10px captions.
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
        : 'bg-ink-3'

  return (
    <div className="pointer-events-auto w-[292px] overflow-hidden rounded-xl border border-hairline bg-card/95 shadow-lifted backdrop-blur">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-left transition-colors hover:bg-surface-2"
        title={open ? '收起指挥官状态' : '展开指挥官状态'}
      >
        <Bot className="h-4 w-4 shrink-0 text-ai" />
        <span className="tw-section-title">指挥官</span>
        <span className="tw-nums ml-auto font-mono text-[11px] text-ink-3">
          {dec?.rounds ? `第 ${fmtInt(dec.rounds)} 轮` : '待命'}
        </span>
        {open ? (
          <ChevronDown className="h-4 w-4 shrink-0 text-ink-3" />
        ) : (
          <ChevronUp className="h-4 w-4 shrink-0 text-ink-3" />
        )}
      </button>

      {open && (
        <div className="space-y-2 border-t border-hairline px-3 py-2.5">
          <div className="flex items-center gap-2">
            <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dot}`} />
            <span className="text-[12px] font-medium text-ink">{PHASE_LABEL[phase] ?? phase}</span>
            {active && since > 0 && (
              <span className="tw-nums font-mono text-[11px] text-ink-3">{elapsed}s</span>
            )}
            {!active && dec?.uptime_seconds ? (
              <span className="tw-nums ml-auto font-mono text-[11px] text-ink-3">
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

          <div className="flex items-center gap-1.5 text-[11px] text-ink-3">
            {activity?.wake && (
              <span className="tw-nums shrink-0 rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-ink-2">
                {activity.wake}
              </span>
            )}
            <span className="min-w-0 flex-1 truncate" title={detail}>
              {detail}
            </span>
          </div>

          {running.length > 0 && (
            <div className="space-y-1.5 border-t border-hairline pt-2">
              <div className="flex items-center justify-between text-[11px] text-ink-3">
                <span>
                  在跑 <span className="tw-nums font-mono font-semibold text-ink-2">{running.length}</span>
                </span>
                {dec?.model && <span className="truncate normal-case tracking-normal">{dec.model}</span>}
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
                      <span className="truncate text-ink-2" title={item.title}>
                        {item.title}
                      </span>
                      <span className="tw-nums ml-auto shrink-0 font-mono text-[11px] text-ink-3">
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
          <div className="flex items-center gap-1.5 border-t border-hairline pt-2 text-[11px] text-ink-3">
            <span className="tw-nums font-mono">决策 {fmtInt(dec?.tokens_total)} tok</span>
            <button
              type="button"
              className="ml-auto rounded text-primary transition-colors hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
              onClick={onOpen}
            >
              决策 →
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
