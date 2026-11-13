import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import {
  Badge,
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Field,
  SkeletonList,
  Textarea,
} from '@cyber/ui'
import { ApiError, api } from '@/lib/api'
import { statusLabel } from '@/lib/format'
import { useLoad } from '@/lib/hooks'
import type { RetryResult } from '@/lib/types'

/**
 * Retry one failed/parked direction. The two shapes differ in what happens to
 * the harness session, not in what the ledger records: resume continues the
 * recorded session (memory kept), a new session drops it. Both re-inject the
 * unconfirmed messages; the preview numbers say how many.
 */
export function RetryIntentDialog({
  open,
  onOpenChange,
  intentId,
  title,
  onRetried,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  intentId: string
  title?: string
  onRetried: (r: RetryResult) => void
}) {
  const preview = useLoad(
    () => (open && intentId ? api.retryPreview(intentId) : Promise.resolve(null)),
    [open, intentId],
  )
  const [note, setNote] = useState('')
  const [choice, setChoice] = useState<'resume' | 'new' | ''>('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [errorCode, setErrorCode] = useState('')

  // A fresh open is a fresh decision: clear the note and re-derive the default.
  useEffect(() => {
    if (!open) return
    setNote('')
    setError('')
    setErrorCode('')
    setChoice('')
  }, [open, intentId])

  // Default to resume when it is available; otherwise new session.
  useEffect(() => {
    if (!open || !preview.data || choice) return
    setChoice(preview.data.can_resume ? 'resume' : 'new')
  }, [open, preview.data, choice])

  const data = preview.data
  const resumeDisabled = !data || !data.can_resume
  const canSubmit = Boolean(data) && Boolean(choice)

  async function submit() {
    if (!choice || !data) return
    setBusy(true)
    setError('')
    setErrorCode('')
    try {
      const result = await api.retryIntent(intentId, {
        note: note.trim() || undefined,
        resume: choice === 'resume',
      })
      onOpenChange(false)
      onRetried(result)
    } catch (e) {
      const code = e instanceof ApiError ? e.code : undefined
      setErrorCode(code ?? '')
      setError(e instanceof Error ? e.message : String(e))
      // The session disappeared between preview and submit: point the operator
      // at the option that still works instead of leaving a dead choice.
      if (code === 'resume_unavailable') setChoice('new')
    } finally {
      setBusy(false)
    }
  }

  function optionCard(selected: boolean, disabled: boolean, onClick: () => void, body: ReactNode) {
    return (
      <button
        type="button"
        aria-pressed={selected}
        disabled={disabled}
        onClick={onClick}
        className={
          'w-full rounded-lg border px-3 py-2 text-left transition-colors ' +
          (disabled
            ? 'cursor-not-allowed border-border bg-muted/30 opacity-60'
            : selected
              ? 'border-primary bg-primary/10'
              : 'border-border bg-card hover:border-primary/40')
        }
      >
        {body}
      </button>
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>重试意图</DialogTitle>
          <DialogDescription>
            {title ? `「${title}」` : '这条意图'}失败/挂起后有两种重试方式：续跑会保留原会话的记忆，
            重派会丢弃会话记忆重新开始；两种都会重投尚未确认的消息。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2.5">
          {preview.loading && <SkeletonList count={2} />}
          {preview.error && (
            <Callout tone="destructive">
              读不到重试预览（retry-preview）：{preview.error}
            </Callout>
          )}

          {data && (
            <>
              <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
                <span>当前状态</span>
                <Badge size="sm" variant="secondary">
                  {statusLabel(data.status)}
                </Badge>
                {data.resume_session && (
                  <span className="font-mono" title="失败的那次 run 记录的 harness session">
                    session {data.resume_session}
                  </span>
                )}
              </div>

              {optionCard(
                choice === 'resume',
                resumeDisabled,
                () => setChoice('resume'),
                <>
                  <div className="flex items-center gap-2 text-[13px] font-medium text-foreground">
                    续跑原会话
                    {!resumeDisabled && (
                      <Badge size="sm" variant="primary">
                        保留记忆
                      </Badge>
                    )}
                  </div>
                  <div className="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">
                    继续原 harness session：失败前的对话、工具结果与上下文都还在，适合“只差最后一步”的方向。
                  </div>
                  <div className="mt-1 font-mono text-[11px] text-info">
                    续跑后将重投 {data.resume_redeliver} 条可能未读消息
                  </div>
                  {resumeDisabled && (
                    <div className="mt-1 text-[11px] text-destructive">
                      不可用：{data.resume_error || '原会话已不可用'}
                    </div>
                  )}
                </>,
              )}

              {optionCard(
                choice === 'new',
                !data,
                () => setChoice('new'),
                <>
                  <div className="flex items-center gap-2 text-[13px] font-medium text-foreground">
                    重派新会话
                    <Badge size="sm" variant="outline">
                      干净重来
                    </Badge>
                  </div>
                  <div className="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">
                    丢弃该 run 的会话记忆，从同一条意图重新开始；适合会话已损坏、或想换一条路径的方向。
                  </div>
                  <div className="mt-1 font-mono text-[11px] text-warning">
                    将丢弃会话记忆，重投 {data.new_session_redeliver} 条未确认消息
                  </div>
                </>,
              )}

              <Field
                label="备注（可选，最多 500 字）"
                hint="只给操作者/下一轮决策看，不会当作执行约束；重试会写入意图备注。"
              >
                <div className="space-y-1">
                  <Textarea
                    rows={3}
                    value={note}
                    maxLength={500}
                    onChange={(e) => setNote(e.target.value)}
                    placeholder="例如：上次卡在登录后重定向，这次从 /portal/login 登录口继续"
                  />
                  <div className="text-right font-mono text-[10px] text-muted-foreground">{note.length}/500</div>
                </div>
              </Field>
            </>
          )}

          {error && (
            <Callout tone="destructive">
              <div className="space-y-0.5">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span>{error}</span>
                  {errorCode && (
                    <Badge size="sm" variant="danger" className="font-mono">
                      {errorCode}
                    </Badge>
                  )}
                </div>
                {errorCode === 'resume_unavailable' && (
                  <div className="text-[11px]">原会话已不可用，请改选「重派新会话」。</div>
                )}
                {errorCode === 'retry_conflict' && (
                  <div className="text-[11px]">这条意图正在跑或已被别的重试占用：刷新后再试。</div>
                )}
                {errorCode === 'retry_refused' && (
                  <div className="text-[11px]">后端拒绝重试（状态不允许）：先重开或处理上一个 run。</div>
                )}
              </div>
            </Callout>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={() => void submit()} disabled={busy || !canSubmit}>
            {busy ? '提交中…' : choice === 'resume' ? '续跑原会话' : choice === 'new' ? '重派新会话' : '选择方式'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
