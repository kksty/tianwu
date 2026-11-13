// Management view for persistent instructions: list, correct, revoke/restore
// and history. Creation lives ONLY in the single 下发 box (choose 持续到撤销),
// so this panel is not a second entry point with its own target picker.
//
// Mutations always use the version the operator opened, never a polled newer
// version; a project change retires outstanding page/history requests.
import { useEffect, useRef, useState } from 'react'
import { FileText, Loader2 } from 'lucide-react'
import { Button, Callout, EmptyState, SkeletonList, Textarea } from '@cyber/ui'
import { api } from '@/lib/api'
import { useConsoleLocation } from '@/lib/hooks'
import { fmtTime } from '@/lib/format'
import type { Instruction } from '@/lib/types'

export function InstructionsPanel() {
  const location = useConsoleLocation()
  return <InstructionWorkspace key={location.pathname} />
}

function InstructionWorkspace() {
  const [page, setPage] = useState<Awaited<ReturnType<typeof api.instructions>>>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [selected, setSelected] = useState<Instruction>()
  const [draft, setDraft] = useState('')
  const [history, setHistory] = useState<Awaited<ReturnType<typeof api.instructionHistory>>>()
  const generation = useRef(0)
  const active = useRef(true)

  useEffect(() => {
    active.current = true
    void load()
    return () => { active.current = false; generation.current++ }
  }, [])

  async function load(cursor = '') {
    const request = ++generation.current
    setBusy(true); setError('')
    try {
      const next = await api.instructions(cursor)
      if (!active.current || request !== generation.current) return
      setPage(old => cursor && old ? {...next, instructions: [...old.instructions, ...next.instructions]} : next)
    } catch (e) { if (active.current && request === generation.current) setError(String(e)) }
    finally { if (active.current && request === generation.current) setBusy(false) }
  }

  /** Open one existing instruction for correction/history. There is deliberately
   *  no "new" path: creation is the 下发 box's 持续到撤销 choice. */
  function edit(row: Instruction) {
    generation.current++
    setSelected(row); setDraft(row.text); setHistory(undefined); setError('')
  }

  async function save(state: Instruction['state'] = 'active') {
    if (!selected || busy || !draft.trim()) return
    setBusy(true); setError('')
    const request = ++generation.current
    try {
      await api.updateInstruction(selected.id, {version: selected.version, text: draft.trim(), state})
      if (!active.current || request !== generation.current) return
      setSelected(undefined); setDraft(''); setHistory(undefined)
      await load()
    } catch (e) { if (active.current && request === generation.current) setError(String(e)) }
    finally { if (active.current) setBusy(false) }
  }

  async function readHistory(cursor = '') {
    if (!selected || busy) return
    const request = ++generation.current
    setBusy(true); setError('')
    try {
      const next = await api.instructionHistory(selected.id, cursor)
      if (!active.current || request !== generation.current) return
      setHistory(old => cursor && old ? {...next, history: [...old.history, ...next.history]} : next)
    } catch (e) { if (active.current && request === generation.current) setError(String(e)) }
    finally { if (active.current && request === generation.current) setBusy(false) }
  }

  const loaded = page?.instructions.length ?? 0

  return (
    <div className="flex h-full min-h-0 flex-col" data-instructions-panel>
      <div className="shrink-0 border-b border-hairline px-3 py-2 text-[12px] leading-5 text-ink-2">
        在下方「下发」框选择「持续到撤销」创建持久指令；这里查看、更正、撤销，并保留修订历史。持久指令会被后续新任务继承，保存或确认均不代表已执行。
      </div>

      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline px-3 py-2">
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => void load()}>刷新列表</Button>
        <span className="tw-nums font-mono text-[11px] text-ink-3">已载入 {loaded} / {page?.total ?? '—'}</span>
        {error && (
          <Button size="sm" variant="outline" className="ml-auto" disabled={busy} onClick={() => void load()}>
            重试
          </Button>
        )}
      </div>

      <div className="grid min-h-0 flex-1 grid-cols-1 lg:grid-cols-2">
        <div className="min-h-0 overflow-y-auto border-b border-hairline p-2 lg:border-b-0 lg:border-r">
          {error && (
            <div role="alert" className="p-1">
              <Callout tone="destructive">读取持久指令失败：{error}</Callout>
            </div>
          )}
          {busy && !page && <div className="p-1"><SkeletonList count={4} /></div>}
          {page?.instructions.length === 0 && (
            <EmptyState
              compact
              icon={FileText}
              title="还没有持久指令"
              description="在「下发」框选择「持续到撤销」创建，之后会出现在这里，可更正或撤销。"
            />
          )}
          <div className="space-y-1">
            {page?.instructions.map(row => (
              <button
                key={row.id}
                type="button"
                disabled={busy}
                onClick={() => edit(row)}
                aria-pressed={selected?.id === row.id}
                className={`block w-full rounded-lg px-2.5 py-2.5 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-40 ${
                  selected?.id === row.id
                    ? 'bg-primary/[0.09] shadow-[inset_2px_0_0_var(--primary)]'
                    : 'hover:bg-surface-2'
                }`}
              >
                <div className="mb-1 flex flex-wrap items-center gap-2 text-[11px] text-ink-3">
                  <span className={`font-medium ${row.state === 'active' ? 'text-success' : 'text-ink-3'}`}>
                    {row.state === 'active' ? '有效' : '已撤销'}
                  </span>
                  <span className="tw-nums font-mono">v{row.version}</span>
                  <span className="truncate">{row.intent_id ? `方向及后代 · ${row.intent_id}` : '整个项目'}</span>
                  <span className="ml-auto shrink-0 font-mono">{fmtTime(row.updated_at)}</span>
                </div>
                <p className="tw-break line-clamp-3 whitespace-pre-wrap text-[12px] leading-relaxed text-ink-2">{row.text}</p>
              </button>
            ))}
          </div>
          {page?.has_more && (
            <div className="px-1 pt-2">
              <Button size="sm" variant="outline" disabled={busy} onClick={() => void load(page.next_cursor)}>
                {busy ? '加载中…' : '继续加载'}
              </Button>
            </div>
          )}
        </div>

        <div className="min-h-0 overflow-y-auto p-3">
          {!selected ? (
            <div className="space-y-2">
              <p className="text-[12px] leading-relaxed text-ink-2">
                在左侧选择一条指令查看或更正；新建请用「下发」框（持续到撤销）。
              </p>
            </div>
          ) : (
            <div className="space-y-2.5">
              <div className="flex flex-wrap items-center gap-2">
                <span className="tw-section-title">更正指令</span>
                <span className="tw-nums rounded border border-hairline bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-ink-3">
                  v{selected.version}
                </span>
              </div>
              <div className="tw-inset rounded-md px-2.5 py-2 text-[11px] leading-4 text-ink-3">
                适用范围（只读）：{selected.intent_id ? `方向及后代 · ${selected.intent_id}` : '整个项目'}
              </div>
              <Textarea aria-label="持久指令正文" rows={6} maxLength={4000} disabled={busy} value={draft} onChange={e => setDraft(e.target.value)} placeholder="更正持续有效的指导或约束" className="text-[12px] leading-relaxed" />
              <div className="flex flex-wrap items-center gap-2">
                <Button size="sm" disabled={busy || !draft.trim()} onClick={() => void save()}>
                  {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                  {selected.state === 'revoked' ? '恢复并保存' : '保存更正'}
                </Button>
                {selected.state === 'active' && (
                  <Button size="sm" variant="outline" disabled={busy} onClick={() => void save('revoked')}>撤销指令</Button>
                )}
                <Button size="sm" variant="ghost" disabled={busy} onClick={() => void readHistory()}>修订历史</Button>
              </div>
              <p className="text-[11px] leading-4 text-ink-3">修改基于打开时的版本；并发更正将提示冲突，请刷新列表后重新打开。</p>
              {history && (
                <div className="space-y-2 border-t border-hairline pt-2">
                  {history.history.map(row => (
                    <div key={row.version} className="rounded-md bg-surface-2 p-2">
                      <p className="mb-1 flex flex-wrap items-center gap-2 text-[11px] text-ink-3">
                        <span className="tw-nums font-mono">v{row.value.version}</span>
                        <span>{row.value.state === 'active' ? '有效' : '已撤销'}</span>
                        <span className="ml-auto font-mono">{fmtTime(row.at)}</span>
                      </p>
                      <p className="tw-break whitespace-pre-wrap text-[12px] leading-relaxed text-ink-2">{row.value.text}</p>
                    </div>
                  ))}
                  {history.has_more && (
                    <Button size="sm" variant="ghost" disabled={busy} onClick={() => void readHistory(history.next_cursor)}>
                      继续读取历史
                    </Button>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
