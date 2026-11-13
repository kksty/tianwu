import { useMemo, useState } from 'react'
import { Bot, ChevronRight, Wrench } from 'lucide-react'
import { fmtInt } from '@/lib/format'
import {
  buildRunReview,
  fmtReviewElapsed,
  orderReviewEntries,
  type ReviewEntry,
  type ReviewOrder,
  type ReviewState,
} from '@/lib/run-review'
import type { Event } from '@/lib/types'

const STATE_LABEL: Record<ReviewState, string> = {
  completed: '完成',
  failed: '失败',
  running: '进行中',
  interrupted: '未收到结果',
}

const STATE_CLASS: Record<ReviewState, string> = {
  completed: 'text-success',
  failed: 'text-destructive',
  running: 'text-info',
  interrupted: 'text-warning',
}

/**
 * The execution review sits above the raw event stream: one row per tool
 * invocation (evidence, elapsed, success) and per reported model call (tokens,
 * cache hit, retries), sortable by time or tokens. Clicking a row highlights
 * the event stream row the evidence came from; the stream itself stays the
 * source of truth and keeps rendering every recorded event.
 */
export function RunReview({
  events,
  totalTokens,
  focusIndex,
  onSelect,
}: {
  events: Event[]
  /** The run record's token total; the event tail may be partial. */
  totalTokens?: number
  /** Event row currently highlighted in the stream. */
  focusIndex?: number | null
  /** Highlight and scroll the stream to one event row. */
  onSelect: (eventIndex: number) => void
}) {
  const [open, setOpen] = useState(false)
  const [order, setOrder] = useState<ReviewOrder>('time')
  const review = useMemo(() => buildRunReview(events), [events])
  const ordered = useMemo(() => orderReviewEntries(review.entries, order), [review, order])
  // The run record is the authoritative whole-run number; the review entries
  // only see the journal tail. They are the same recorded usage, not a guess.
  const tokens = totalTokens !== undefined && totalTokens > 0 ? totalTokens : review.totalTokens
  const counts = (Object.keys(STATE_LABEL) as ReviewState[])
    .filter((state) => review.counts[state] > 0)
    .map((state) => `${STATE_LABEL[state]} ${review.counts[state]}`)
  const tokenTitle =
    totalTokens !== undefined && totalTokens > 0 && totalTokens !== review.totalTokens
      ? `运行记录 ${fmtInt(totalTokens)} tok；本段事件合计 ${fmtInt(review.totalTokens)} tok`
      : undefined

  return (
    <details
      className="mb-1 rounded-lg border border-border/60 bg-background/40"
      data-testid="run-review"
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary className="flex cursor-pointer list-none items-center gap-2 px-2 py-1.5 text-[12px] [&::-webkit-details-marker]:hidden">
        <ChevronRight
          className={`h-3 w-3 shrink-0 text-muted-foreground transition-transform ${open ? 'rotate-90' : ''}`}
        />
        <span className="font-medium text-foreground/85">执行审查</span>
        {counts.length > 0 && <span className="truncate text-muted-foreground">{counts.join(' · ')}</span>}
        <span className="ml-auto shrink-0 font-mono text-[11px] text-muted-foreground" title={tokenTitle}>
          {fmtInt(tokens)} tok
        </span>
      </summary>
      {open && (
        <>
          <div
            className="flex items-center gap-1 border-t border-border/50 px-2 py-1"
            role="group"
            aria-label="复盘排序"
          >
            <span className="text-[10px] text-muted-foreground">排序</span>
            {(['time', 'tokens'] as const).map((value) => (
              <button
                key={value}
                type="button"
                aria-pressed={order === value}
                onClick={() => setOrder(value)}
                className={`rounded px-1.5 py-0.5 text-[11px] transition-colors ${
                  order === value ? 'bg-primary/10 text-primary' : 'text-muted-foreground hover:bg-accent'
                }`}
              >
                {value === 'time' ? '时间' : '令牌'}
              </button>
            ))}
            <span className="ml-auto text-[10px] text-muted-foreground">{review.entries.length} 项</span>
          </div>
          {review.entries.length === 0 ? (
            <div className="border-t border-border/50 px-2 py-2 text-[11px] text-muted-foreground">
              这段日志里还没有工具调用或模型用量。
            </div>
          ) : (
            <ol className="border-t border-border/50 p-1">
              {ordered.map((entry) => (
                <li key={entry.key} data-review-key={entry.key} data-state={entry.state}>
                  <ReviewRow entry={entry} selected={focusIndex === entry.eventIndex} onSelect={onSelect} />
                </li>
              ))}
            </ol>
          )}
          <p className="border-t border-border/50 px-2 py-1 text-[10px] leading-relaxed text-muted-foreground">
            点击条目定位到原始事件。仅统计已报告的模型用量；工具返回不代表验证通过，未收到结果不代表未执行。
          </p>
        </>
      )}
    </details>
  )
}

function ReviewRow({
  entry,
  selected,
  onSelect,
}: {
  entry: ReviewEntry
  selected: boolean
  onSelect: (eventIndex: number) => void
}) {
  const Icon = entry.kind === 'tool' ? Wrench : Bot
  const hasUsage = entry.inputTokens !== undefined || entry.outputTokens !== undefined
  return (
    <button
      type="button"
      onClick={() => onSelect(entry.eventIndex)}
      aria-pressed={selected}
      className={`flex w-full items-start gap-2 rounded px-1.5 py-1 text-left transition-colors ${
        selected ? 'bg-primary/10' : 'hover:bg-accent/60'
      }`}
    >
      <span className="mt-0.5 w-4 shrink-0 text-right font-mono text-[10px] text-muted-foreground">
        {entry.order}
      </span>
      <Icon className={`mt-0.5 h-3 w-3 shrink-0 ${STATE_CLASS[entry.state]}`} />
      <span className="min-w-0 flex-1">
        <span className="flex flex-wrap items-center gap-x-1.5">
          <span className="truncate text-[11.5px] font-medium text-foreground/85">{entry.label}</span>
          <span className={`shrink-0 text-[10px] ${STATE_CLASS[entry.state]}`}>{STATE_LABEL[entry.state]}</span>
        </span>
        {entry.evidence && (
          <span className="tw-break mt-0.5 block text-[11px] leading-snug text-muted-foreground">
            {entry.evidence}
          </span>
        )}
      </span>
      <span className="flex shrink-0 flex-col items-end gap-0.5 font-mono text-[10px] leading-tight text-muted-foreground">
        {entry.kind === 'tool' && entry.elapsedMs !== undefined && <span>{fmtReviewElapsed(entry.elapsedMs)}</span>}
        {hasUsage && (
          <span>
            ↓{fmtInt(entry.inputTokens ?? 0)} ↑{fmtInt(entry.outputTokens ?? 0)}
          </span>
        )}
        {entry.cacheHitPercent !== undefined && (
          <span
            title={
              entry.cachedInputTokens !== undefined
                ? `缓存输入 ${fmtInt(entry.cachedInputTokens)} tok`
                : undefined
            }
          >
            缓存 {Math.round(entry.cacheHitPercent)}%
          </span>
        )}
        {entry.requests !== undefined && entry.requests > 1 && (
          <span className="text-warning">{entry.requests} 次请求（含重试）</span>
        )}
        {entry.kind === 'model' && <span>{fmtInt(entry.totalTokens)} tok</span>}
      </span>
    </button>
  )
}
