import { useState } from 'react'
import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Field,
  Input,
  Textarea,
} from '@cyber/ui'
import { api } from '@/lib/api'

/** A goal is the statement the engagement must prove; it is never auto-scored. */
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
  const [quantified, setQuantified] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function submit() {
    if (!statement.trim()) {
      setError('目标不能为空：它要能判断真假')
      return
    }
    setBusy(true)
    setError('')
    try {
      await api.createGoal(statement.trim(), quantified.trim())
      setStatement('')
      setQuantified('')
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
            写成一句可以判断真假的话。盖章（标记达成）必须挂一条真实证据，否则会被事实意图图拒绝。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <Field label="目标">
            <Textarea
              rows={3}
              value={statement}
              onChange={(e) => setStatement(e.target.value)}
              placeholder="证明外网门户的 /admin 存在未授权访问，并留下原始响应"
              autoFocus
            />
          </Field>
          <Field label="量化条件" hint="可选：用来判断'达成'的具体数字或清单">
            <Input
              value={quantified}
              onChange={(e) => setQuantified(e.target.value)}
              placeholder="例如：至少 1 条 vuln 带证据；或 80/443/8080 三个端口都测过"
            />
          </Field>
          {error && <div className="text-[12px] text-destructive">{error}</div>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy}>
            {busy ? '创建中…' : '创建'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
