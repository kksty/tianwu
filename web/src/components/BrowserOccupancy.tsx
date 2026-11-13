import { useState } from 'react'
import { LockKeyhole, RotateCcw, UnlockKeyhole } from 'lucide-react'
import { Badge, Button, Textarea } from '@cyber/ui'
import { browserOccupancyLabel, canOperateBrowser } from '@/lib/access'
import { fmtTime } from '@/lib/format'
import type { AccessEntry } from '@/lib/types'

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
  return <div className="mb-3 rounded-lg border border-border/70 bg-muted/25 p-3">
    <div className="flex flex-wrap items-center justify-between gap-2">
      <Badge variant={!lease ? 'muted' : lease.state === 'recovery' ? 'warning' : canOperateBrowser(entry) ? 'info' : 'secondary'}>
        {lease ? <LockKeyhole className="mr-1 h-3 w-3" /> : <UnlockKeyhole className="mr-1 h-3 w-3" />}{browserOccupancyLabel(entry)}
      </Badge>
      {canOperateBrowser(entry) && <Button size="sm" variant="ghost" disabled={busy} onClick={() => onChange('release')}>归还现场</Button>}
      {lease?.state === 'recovery' && !lease.busy && <Button size="sm" variant="outline" disabled={busy} onClick={() => setRecovering(!recovering)}><RotateCcw className="mr-1 h-3 w-3" />确认恢复</Button>}
    </div>
    {lease && <div className="mt-2 space-y-1 text-[11px] leading-relaxed text-muted-foreground">
      <p className="break-all">持有者 {lease.owner}{lease.generation ? ` · 第 ${lease.generation} 代` : ''}</p>
      <p>领取于 {fmtTime(lease.acquired_at)}{lease.run_id && <> · <a className="text-primary hover:underline" href={`?view=runs&run=${encodeURIComponent(lease.run_id)}`}>查看持有者运行</a></>}</p>
      {lease.note && <p className="whitespace-pre-wrap break-words">{lease.note}</p>}
    </div>}
    {lease && !canOperateBrowser(entry) && <p className="mt-2 text-[11px] leading-relaxed text-muted-foreground">{lease.busy ? '浏览器操作进行中，当前不能归还、恢复或删除现场。' : lease.state === 'recovery' ? '运行已结束或命令结果未知；确认原现场无在途操作后恢复。不会按超时自动转给新执行者。' : '现场由执行者使用中。它归还前，控制台暂停浏览器操作；需要接手时先停止持有者运行。'}</p>}
    {recovering && lease?.state === 'recovery' && <div className="mt-3 space-y-3 border-t border-border/70 pt-3">
      <Textarea aria-label={`恢复说明：${entry.name}`} placeholder="已检查原窗口，说明无在途操作的依据" rows={2} maxLength={500} disabled={busy} value={note} onChange={event => setNote(event.target.value)} />
      <label className="flex items-start gap-2 text-xs leading-relaxed text-muted-foreground"><input type="checkbox" className="mt-0.5 accent-primary" disabled={busy} checked={confirmed} onChange={event => setConfirmed(event.target.checked)} />我已确认原操作结束，浏览器现场可以交还。</label>
      <Button size="sm" disabled={busy || !confirmed || !note.trim()} onClick={recover}>恢复为空闲</Button>
    </div>}
  </div>
}
