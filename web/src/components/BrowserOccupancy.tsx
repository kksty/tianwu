import { useState } from 'react'
import { LockKeyhole, RotateCcw, UnlockKeyhole } from 'lucide-react'
import { Badge, Button, Textarea } from '@cyber/ui'
import { browserOccupancyLabel, canOperateBrowser } from '@/lib/access'
import { fmtTime } from '@/lib/format'
import type { AccessEntry } from '@/lib/types'

/**
 * The occupancy strip inside an access card. It owns the whole "who is holding
 * this browser right now" question: one status line, the lease facts in mono,
 * and the two actions that can change it. It is visually recessed (an inset
 * block on the card), so "the material exists" and "someone is using it" read
 * as two different facts instead of one busy row.
 */
export function BrowserOccupancy({ entry, busy, onChange }: {
  entry: AccessEntry
  busy: boolean
  onChange: (action: 'acquire' | 'release' | 'recover', note?: string) => Promise<boolean>
}) {
  const [recovering, setRecovering] = useState(false)
  const [note, setNote] = useState('')
  const [confirmed, setConfirmed] = useState(false)
  const lease = entry.lease
  async function recover() {
    if (await onChange('recover', note)) { setRecovering(false); setNote(''); setConfirmed(false) }
  }

  const variant = !lease
    ? 'muted'
    : lease.state === 'recovery'
      ? 'warning'
      : canOperateBrowser(entry)
        ? 'info'
        : 'secondary'

  return (
    <div className="tw-inset mb-3 rounded-lg p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="tw-eyebrow">现场占用</span>
        <Badge variant={variant} size="sm">
          {lease ? <LockKeyhole className="mr-1 h-3 w-3" /> : <UnlockKeyhole className="mr-1 h-3 w-3" />}
          {browserOccupancyLabel(entry)}
        </Badge>
        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          {canOperateBrowser(entry) && (
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => onChange('release')}>
              归还现场
            </Button>
          )}
          {lease?.state === 'recovery' && !lease.busy && (
            <Button
              size="sm"
              variant={recovering ? 'soft' : 'outline'}
              disabled={busy}
              active={recovering}
              onClick={() => setRecovering(!recovering)}
            >
              <RotateCcw className="mr-1 h-3.5 w-3.5" /> 确认恢复
            </Button>
          )}
        </div>
      </div>

      {lease ? (
        <dl className="mt-2.5 grid grid-cols-1 gap-x-4 gap-y-1 text-[12px] leading-relaxed sm:grid-cols-2">
          <div className="flex min-w-0 items-baseline gap-1.5">
            <dt className="shrink-0 text-[11px] text-ink-3">持有者</dt>
            <dd className="tw-break min-w-0 font-mono text-[11px] text-ink-2">
              {lease.owner}
              {lease.generation ? ` · 第 ${lease.generation} 代` : ''}
            </dd>
          </div>
          <div className="flex min-w-0 items-baseline gap-1.5">
            <dt className="shrink-0 text-[11px] text-ink-3">领取于</dt>
            <dd className="tw-break min-w-0 font-mono text-[11px] text-ink-2">
              <span className="tw-nums">{fmtTime(lease.acquired_at)}</span>
              {lease.run_id && (
                <>
                  {' · '}
                  <a
                    className="text-info hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                    href={`?view=runs&run=${encodeURIComponent(lease.run_id)}`}
                  >
                    查看持有者运行
                  </a>
                </>
              )}
            </dd>
          </div>
          {lease.note && (
            <div className="flex min-w-0 items-baseline gap-1.5 sm:col-span-2">
              <dt className="shrink-0 text-[11px] text-ink-3">说明</dt>
              <dd className="tw-break min-w-0 whitespace-pre-wrap text-[12px] text-ink-2">{lease.note}</dd>
            </div>
          )}
        </dl>
      ) : (
        <p className="mt-2 text-[12px] leading-relaxed text-ink-2">
          现场空闲：需要操作时先领取。
        </p>
      )}

      {lease && !canOperateBrowser(entry) && (
        <p className="mt-2 text-[11px] leading-relaxed text-ink-3">
          {lease.busy
            ? '浏览器操作进行中，当前不能归还、恢复或删除现场。'
            : lease.state === 'recovery'
              ? '运行已结束或命令结果未知；确认原现场无在途操作后恢复。不会按超时自动转给新执行者。'
              : '现场由执行者使用中。它归还前，控制台暂停浏览器操作；需要接手时先停止持有者运行。'}
        </p>
      )}

      {recovering && lease?.state === 'recovery' && (
        <div className="mt-3 space-y-3 border-t border-hairline pt-3">
          <Textarea
            aria-label={`恢复说明：${entry.name}`}
            placeholder="已检查原窗口，说明无在途操作的依据"
            rows={2}
            maxLength={500}
            disabled={busy}
            value={note}
            onChange={event => setNote(event.target.value)}
            className="text-[12px]"
          />
          <label className="flex items-start gap-2 text-[12px] leading-relaxed text-ink-2">
            <input
              type="checkbox"
              className="mt-0.5 accent-primary"
              disabled={busy}
              checked={confirmed}
              onChange={event => setConfirmed(event.target.checked)}
            />
            我已确认原操作结束，浏览器现场可以交还。
          </label>
          <Button size="sm" disabled={busy || !confirmed || !note.trim()} onClick={recover}>
            恢复为空闲
          </Button>
        </div>
      )}
    </div>
  )
}
