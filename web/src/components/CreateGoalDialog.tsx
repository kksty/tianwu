import { useState } from 'react'
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
  Spinner,
  Textarea,
} from '@cyber/ui'
import { api } from '@/lib/api'

/**
 * A goal is the endpoint the operator defines for the engagement; it is never
 * auto-scored and the commander does not create it.
 *
 * The dialog is a two-field form: 起点 (where the work begins — context for the
 * commander) and 终点 (the falsifiable end state). The seal/达成 flow that
 * attaches evidence lives on the overview; this dialog only writes the goal into
 * the ledger.
 */
export function CreateGoalDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: () => void
}) {
  const [statement, setStatement] = useState('')
  const [start, setStart] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function submit() {
    if (busy) return
    if (!statement.trim()) {
      setError('终点不能为空：它要能判断真假')
      return
    }
    setBusy(true)
    setError('')
    try {
      await api.createGoal(statement.trim(), start.trim())
      setStatement('')
      setStart('')
      onOpenChange(false)
      onCreated()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>新建目标</DialogTitle>
          <DialogDescription>
            目标由你定义：起点说明现在从哪里开始，终点写成一句可以判断真假的话。盖章（标记达成）必须挂一条真实证据，否则会被事实意图图拒绝。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <Field label="起点" hint="可选：现在的状态、已知资产或已经确认的部分；会交给指挥官作为判断背景">
            <Textarea
              rows={2}
              value={start}
              onChange={(e) => setStart(e.target.value)}
              placeholder="例如：已确认门户首页与登录口，有一个测试账号"
              autoFocus
              disabled={busy}
            />
          </Field>
          <Field label="终点" hint="必填：要达成的、能判断真假的状态">
            <Textarea
              rows={3}
              value={statement}
              onChange={(e) => setStatement(e.target.value)}
              placeholder="证明外网门户的 /admin 存在未授权访问，并留下原始响应"
              disabled={busy}
            />
          </Field>
          {error && <Callout tone="destructive">{error}</Callout>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy}>
            {busy ? (
              <>
                <Spinner size="sm" className="mr-1.5" />
                创建中…
              </>
            ) : (
              '创建'
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
