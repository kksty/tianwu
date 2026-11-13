// The explicit "追加目标并继续" operator dialog (internal/api/goal_continue.go).
//
// This is deliberately NOT a chat send: ordinary messages never resume a paused
// project. The dialog appends a NEW independent goal and, when the project is in
// a resumable stand-by (完成待命 or 人工暂停), the server clears that pause and
// wakes the commander. A model/credential fault is refused with a reason and a
// needs-human marker; a failed pause write leaves the goal created and offers a
// retry with the SAME idempotency key, so no second goal is appended.
//
// The key is generated once per submitted draft and reused across retries; it is
// regenerated only when the operator edits the content, because the server
// answers 409 goal_idempotency_conflict for one key naming two different goals.

import { useEffect, useMemo, useRef, useState } from 'react'
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
  Textarea,
} from '@cyber/ui'
import { api } from '@/lib/api'
import {
  appendGoalFailure,
  appendGoalKeyFor,
  appendGoalOutcome,
  appendGoalRequest,
  pauseKindLabel,
  loadPendingAppend,
  savePendingAppend,
  randomAppendGoalKey,
  type AppendGoalDraft,
  type AppendGoalFailure,
  type AppendGoalKey,
  type AppendGoalOutcome,
} from '@/lib/append-goal'
import type { AppendGoalResult } from '@/lib/types'

const EMPTY_DRAFT: AppendGoalDraft = { start: '', statement: '' }

export function AppendGoalDialog({
  open,
  onOpenChange,
  paused,
  pauseReason,
  onCompleted,
  onPartial,
  projectID,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** True when the project is parked (completion stand-by, manual pause, fault). */
  paused: boolean
  /** The decider's human-readable pause reason, when there is one. */
  pauseReason?: string
  /** Called with the server's answer after success; the page refreshes board/goals. */
  onCompleted: (result: AppendGoalResult) => void
  onPartial?: () => void
  projectID: string
}) {
  const pending = useMemo(() => {
    try { return loadPendingAppend(window.sessionStorage, projectID) } catch { return null }
  }, [projectID])
  const [draft, setDraft] = useState<AppendGoalDraft>(pending?.draft ?? EMPTY_DRAFT)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<AppendGoalFailure | null>(pending?.goalId ? { code: 'resume_failed', message: '目标已创建，等待恢复继续。', hint: '可安全重试，系统会恢复同一个目标。', needsHuman: false, retryable: true, goalId: pending.goalId } : null)
  const [outcome, setOutcome] = useState<AppendGoalOutcome | null>(null)
  // One key per submitted draft: survives retries, replaced on any edit.
  const keyRef = useRef<AppendGoalKey | null>(pending?.key ?? null)
  const openRef = useRef(open)
  const active = useRef(true)

  useEffect(() => {
    active.current = true
    return () => {
      active.current = false
    }
  }, [])

  useEffect(() => {
    // Reopening after success starts a fresh draft. A retry left unresolved keeps
    // its draft and key, so reopening continues the same request instead of
    // creating a second goal.
    if (open && !openRef.current && outcome) {
      setDraft(EMPTY_DRAFT)
      setOutcome(null)
      setFailure(null)
      keyRef.current = null
    }
    openRef.current = open
  }, [open, outcome])

  function update(patch: Partial<AppendGoalDraft>) {
    setDraft((current) => ({ ...current, ...patch }))
    // Editing content after a failure must not reuse the old request identity.
    setFailure(null)
  }

  async function submit() {
    if (busy) return
    if (!draft.statement.trim()) {
      setFailure({
        code: 'goal_invalid',
        message: '终点不能为空：它要能判断真假',
        needsHuman: false,
        retryable: false,
      })
      return
    }
    keyRef.current = appendGoalKeyFor(keyRef.current, draft, randomAppendGoalKey)
    try { savePendingAppend(window.sessionStorage, projectID, { draft, key: keyRef.current }) } catch { /* restricted storage */ }
    setBusy(true)
    setFailure(null)
    try {
      const result = await api.appendGoal(appendGoalRequest(draft, keyRef.current.key))
      if (!active.current) return
      // Delivered: the next submission is a different goal and gets a new key.
      keyRef.current = null
      try { savePendingAppend(window.sessionStorage, projectID, null) } catch { /* restricted storage */ }
      setOutcome(appendGoalOutcome(result))
      onCompleted(result)
    } catch (e) {
      if (!active.current) return
      // Keep the same key: a resume_failed answer means the goal already exists.
      const next = appendGoalFailure(e)
      setFailure(next)
      if (next.goalId) {
        try { if (keyRef.current) savePendingAppend(window.sessionStorage, projectID, { draft, key: keyRef.current, goalId: next.goalId }) } catch { /* restricted storage */ }
        onPartial?.()
      }
    } finally {
      if (active.current) setBusy(false)
    }
  }

  const close = () => {
    if (busy) return
    onOpenChange(false)
  }
  const leaveCreatedGoal = () => {
    try { savePendingAppend(window.sessionStorage, projectID, null) } catch { /* restricted storage */ }
    keyRef.current = null
    setDraft(EMPTY_DRAFT)
    setFailure(null)
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={(next) => (busy ? undefined : onOpenChange(next))}>
      <DialogContent className="max-h-[90dvh] max-w-lg overflow-y-auto" data-testid="append-goal-dialog">
        <DialogHeader>
          <DialogTitle>追加目标并继续</DialogTitle>
          <DialogDescription>
            追加以你定义的起点 → 终点为一个新的独立目标；旧目标的完成证明不会被改写。这是项目级显式操作，不是聊天下发——普通消息不会自动恢复暂停的项目。
          </DialogDescription>
        </DialogHeader>

        {outcome ? (
          <div className="space-y-3" data-testid="append-goal-success">
            <Callout tone="success">
              <div className="font-medium">{outcome.title}</div>
              {outcome.note && <div className="mt-1 text-[12px] leading-relaxed">{outcome.note}</div>}
            </Callout>
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-ink-3">
              {outcome.resumed ? (
                <span>
                  已恢复派发（来自「{pauseKindLabel(outcome.resumedFrom)}」）
                </span>
              ) : outcome.pausedAfter ? (
                <span>项目仍在暂停：「{pauseKindLabel(outcome.pauseKindAfter)}」</span>
              ) : (
                <span>项目未暂停：只追加了目标</span>
              )}
              <span>{outcome.deciderWoken ? '指挥官已被唤醒' : '指挥官未被唤醒'}</span>
              <span>新目标是独立目标，不会替代或撤销旧目标</span>
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <Callout tone="info">
              {paused ? (
                <>
                  项目当前暂停
                  {pauseReason ? `（${pauseReason}）` : ''}：提交成功会显式恢复派发并唤醒指挥官；旧目标的
                  完成收据保持不变。
                </>
              ) : (
                <>项目未暂停：只追加目标并唤醒指挥官，不改动在跑方向和等待关系。</>
              )}
            </Callout>

            <Field label="起点" hint="可选：这次从哪里开始（现在的状态、已知资产或上一目标的收口结果）">
              <Textarea
                rows={2}
                value={draft.start}
                onChange={(e) => update({ start: e.target.value })}
                disabled={busy || Boolean(failure?.goalId)}
                placeholder="例如：上一目标已完成：已确认门户入口与一个测试账号"
              />
            </Field>
            <Field label="终点" hint="必填：新目标要达成的、能判断真假的状态">
              <Textarea
                rows={3}
                value={draft.statement}
                onChange={(e) => update({ statement: e.target.value })}
                disabled={busy || Boolean(failure?.goalId)}
                autoFocus
                placeholder="例如：在授权范围内证明 /admin 的未授权访问，并留下原始响应与时间"
              />
            </Field>

            {failure && (
              <div data-testid="append-goal-error">
                <Callout tone="destructive">
                  <div className="text-[12px] font-medium">{failure.message}</div>
                  {failure.pauseReason && (
                    <div className="mt-1 text-[11px] opacity-90">暂停原因：{failure.pauseReason}</div>
                  )}
                  {failure.needsHuman && (
                    <div className="mt-1 text-[11px] opacity-90">
                      需要人工修复：先修复模型 key 或切换 profile，再重试。
                    </div>
                  )}
                  {failure.hint && <div className="mt-1 text-[11px] opacity-90">{failure.hint}</div>}
                  <details className="mt-1 text-[11px] opacity-80"><summary>技术详情</summary><div className="font-mono">
                    {failure.code || 'unknown'}
                    {failure.retryable
                      ? ' · 可安全重试'
                      : failure.code === 'goal_invalid'
                        ? ''
                        : ' · 不可重试'}
                  </div></details>
                </Callout>
              </div>
            )}

            <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-ink-3">
              <span>提交遇到问题后可安全重试，不会重复创建同一目标。</span>
            </div>
          </div>
        )}

        {outcome ? (
          <DialogFooter>
            <Button onClick={close}>完成</Button>
          </DialogFooter>
        ) : (
          <DialogFooter>
            <Button variant="outline" onClick={failure?.goalId ? leaveCreatedGoal : close} disabled={busy}>
              {failure?.goalId ? '保留目标，稍后处理' : failure ? '关闭' : '取消'}
            </Button>
            <Button onClick={submit} disabled={busy}>
              {busy
                ? '提交中…'
                : failure?.retryable
                  ? '重试继续'
                  : '追加目标并继续'}
            </Button>
          </DialogFooter>
        )}
      </DialogContent>
    </Dialog>
  )
}
