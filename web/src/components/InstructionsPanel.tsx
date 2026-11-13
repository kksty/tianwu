// Management view for persistent instructions: list, correct, revoke/restore
// and history. Creation lives ONLY in the single 下发 box (choose 持续到撤销),
// so this panel is not a second entry point with its own target picker.
//
// Mutations always use the version the operator opened, never a polled newer
// version; a project change retires outstanding page/history requests.
import { useEffect, useRef, useState } from 'react'
import { Button, Textarea } from '@cyber/ui'
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

  return <div className="h-full overflow-auto p-3 text-xs" data-instructions-panel>
    <p className="mb-3 text-muted-foreground">
      在下方「下发」框选择「持续到撤销」创建持久指令；这里查看、更正、撤销，并保留修订历史。持久指令会被后续新任务继承，保存或确认均不代表已执行。
    </p>
    <div className="mb-2 flex flex-wrap items-center gap-2">
      <Button size="xs" variant="ghost" disabled={busy} onClick={() => void load()}>刷新列表</Button>
      <span className="text-muted-foreground">已载入 {page?.instructions.length ?? 0} / {page?.total ?? '—'}</span>
    </div>
    <div className="grid gap-3 lg:grid-cols-2">
      <div className="space-y-2">
        {page?.instructions.map(row => <button key={row.id} type="button" disabled={busy} onClick={() => edit(row)} className={`block w-full rounded border p-2 text-left hover:bg-muted/30 ${selected?.id === row.id ? 'border-primary/50 bg-primary/5' : 'border-border'}`}>
          <div className="mb-1 flex flex-wrap gap-2 text-[11px] text-muted-foreground"><span>{row.state === 'active' ? '有效' : '已撤销'} · v{row.version}</span><span>{row.intent_id ? `方向及后代 · ${row.intent_id}` : '整个项目'}</span><span>{fmtTime(row.updated_at)}</span></div>
          <p className="tw-break line-clamp-3 whitespace-pre-wrap">{row.text}</p>
        </button>)}
        {page?.instructions.length === 0 && <p className="text-muted-foreground">还没有持久指令：在「下发」框选择「持续到撤销」创建。</p>}
        {page?.has_more && <Button size="xs" disabled={busy} onClick={() => void load(page.next_cursor)}>继续加载</Button>}
      </div>
      <div className="space-y-2">
        {!selected ? (
          <p className="text-muted-foreground">在左侧选择一条指令查看或更正；新建请用「下发」框（持续到撤销）。</p>
        ) : (
          <>
            <p>更正指令 · v{selected.version}</p>
            <div className="rounded border border-border bg-muted/30 px-2 py-1.5 text-muted-foreground">
              适用范围（只读）：{selected.intent_id ? `方向及后代 · ${selected.intent_id}` : '整个项目'}
            </div>
            <Textarea aria-label="持久指令正文" rows={6} maxLength={4000} disabled={busy} value={draft} onChange={e => setDraft(e.target.value)} placeholder="更正持续有效的指导或约束" />
            <div className="flex flex-wrap gap-2">
              <Button size="xs" disabled={busy || !draft.trim()} onClick={() => void save()}>{selected.state === 'revoked' ? '恢复并保存' : '保存更正'}</Button>
              {selected.state === 'active' && <Button size="xs" variant="outline" disabled={busy} onClick={() => void save('revoked')}>撤销指令</Button>}
              <Button size="xs" variant="ghost" disabled={busy} onClick={() => void readHistory()}>修订历史</Button>
            </div>
            <p className="text-muted-foreground">修改基于打开时的版本；并发更正将提示冲突，请刷新列表后重新打开。</p>
            {history && <div className="space-y-2 border-t border-border pt-2">{history.history.map(row => <div key={row.version} className="rounded bg-muted/30 p-2"><p className="mb-1 text-muted-foreground">v{row.value.version} · {row.value.state === 'active' ? '有效' : '已撤销'} · {fmtTime(row.at)}</p><p className="tw-break whitespace-pre-wrap">{row.value.text}</p></div>)}{history.has_more && <Button size="xs" variant="ghost" disabled={busy} onClick={() => void readHistory(history.next_cursor)}>继续读取历史</Button>}</div>}
          </>
        )}
      </div>
    </div>
    {error && <p role="alert" className="mt-2 text-destructive">{error}</p>}
  </div>
}
