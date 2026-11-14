import { useEffect, useState } from 'react'
import {
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Field,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Spinner,
  Textarea,
} from '@cyber/ui'
import { api } from '@/lib/api'
import type { Evidence } from '@/lib/types'

/** The minimum a seal needs: the goal's identity and the statement to show. */
export interface SealGoalRef {
  id: string
  statement: string
}

/**
 * The one "make this goal met" dialog, shared by the overview goal card and the
 * project pause banner. The server requires a real evidence row and a written
 * reason; the dialog only collects them. It deliberately does not resume the
 * project: sealing a goal and re-arming the commander are separate operator
 * decisions.
 */
export function SealGoalDialog({
  goal,
  open,
  onOpenChange,
  onSealed,
}: {
  goal: SealGoalRef | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onSealed?: () => void
}) {
  const [evidence, setEvidence] = useState<Evidence[]>([])
  const [evidenceID, setEvidenceID] = useState('')
  const [note, setNote] = useState('')
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open || !goal) return
    let cancelled = false
    setEvidence([])
    setEvidenceID('')
    setNote('')
    setError('')
    setLoading(true)
    api
      .evidence()
      .then((rows) => {
        if (!cancelled) setEvidence(rows)
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, goal?.id])

  function close() {
    if (busy) return
    onOpenChange(false)
  }

  async function submit() {
    if (!goal) return
    if (!evidenceID) {
      setError('盖章必须选一条证据')
      return
    }
    const why = note.trim()
    if (!why) {
      setError('盖章必须写一句为什么达成')
      return
    }
    setBusy(true)
    setError('')
    try {
      await api.metGoal(goal.id, evidenceID, why)
      onSealed?.()
      onOpenChange(false)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => (next ? onOpenChange(true) : close())}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>认可达成并收口</DialogTitle>
          <DialogDescription>
            达成必须挂一条真实存在的证据，并写一句为什么这条证据证明了目标。盖章只改变目标状态，项目仍保持暂停；要继续干活请再点「继续当前目标」。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="tw-inset tw-break rounded-lg px-3 py-2 text-[12.5px] leading-relaxed text-ink-2">
            {goal?.statement}
          </div>
          {loading && evidence.length === 0 ? (
            <div className="flex items-center gap-2 rounded-lg border border-hairline bg-surface-2/50 px-3 py-2.5 text-[12px] text-ink-2">
              <Spinner size="sm" />
              正在读取证据…
            </div>
          ) : evidence.length === 0 ? (
            <Callout tone="warning">还没有证据。先跑一轮，等执行者产出原始文件。</Callout>
          ) : (
            <Field label="证据">
              <Select value={evidenceID} onValueChange={setEvidenceID}>
                <SelectTrigger className="h-9 text-[12px]">
                  <SelectValue placeholder="选一条证据" />
                </SelectTrigger>
                <SelectContent>
                  {evidence.map((e) => (
                    <SelectItem key={e.id} value={e.id} className="text-[12px]">
                      <span className="font-mono text-[11px] text-ink-3">{e.id}</span>
                      <span className="ml-2">{e.name}</span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          <Field label="为什么达成" hint="必填：一句话说清这条证据怎样证明目标已达成">
            <Textarea
              rows={2}
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder="例如：/admin 未授权访问已复现，原始响应里带管理员会话 cookie"
            />
          </Field>
          {error && <Callout tone="destructive">{error}</Callout>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={close} disabled={busy}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy || evidence.length === 0 || !evidenceID || !note.trim()}>
            {busy ? (
              <>
                <Spinner size="sm" className="mr-1.5" />
                提交中…
              </>
            ) : (
              '确认达成'
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
