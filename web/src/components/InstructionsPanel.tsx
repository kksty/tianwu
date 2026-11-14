// The persistent-instruction surface: the one place that shows the complete
// set, so the operator can keep it small. Creation, correction and revocation
// all live here; the 下发 box's 「持续到撤销」 stays a shortcut into the same
// rows, never a second kind of record.
//
// Correcting an existing row is the preferred edit: the new text becomes v+1
// and the old text stops being injected into prompts (the revision history
// keeps it). Appending a second sentence about the same topic is what leaves
// two hard constraints in the prompt at once, so "新增" is a deliberate button
// with the current set already on screen.
//
// Mutations always use the version the operator opened, never a polled newer
// version; a project change retires outstanding page/history requests.
import { useEffect, useRef, useState } from 'react'
import { FileText, Loader2, Plus } from 'lucide-react'
import {
  Button,
  Callout,
  EmptyState,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SkeletonList,
  Textarea,
  ToggleGroup,
  ToggleGroupItem,
} from '@cyber/ui'
import { api } from '@/lib/api'
import { useConsoleLocation, usePoll } from '@/lib/hooks'
import { fmtTime } from '@/lib/format'
import { directiveKeyFor, randomRequestKey } from '@/lib/directive'
import {
  createInstructionFingerprint,
  createInstructionRequest,
  instructionErrorMessage,
  instructionScopeLabel,
  sortInstructions,
  type InstructionFilter,
} from '@/lib/instructions'
import type { Instruction } from '@/lib/types'

export function InstructionsPanel({ onChanged }: { onChanged?: () => void }) {
  const location = useConsoleLocation()
  return <InstructionWorkspace key={location.pathname} onChanged={onChanged} />
}

type Page = Awaited<ReturnType<typeof api.instructions>>

function InstructionWorkspace({ onChanged }: { onChanged?: () => void }) {
  const [page, setPage] = useState<Page>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [filter, setFilter] = useState<InstructionFilter>('active')
  const [selected, setSelected] = useState<Instruction>()
  const [draft, setDraft] = useState('')
  const [history, setHistory] = useState<Awaited<ReturnType<typeof api.instructionHistory>>>()
  // Create mode is its own pane: the scope is part of the record and immutable
  // afterwards, so it is chosen deliberately rather than inherited silently.
  const [creating, setCreating] = useState(false)
  const [createText, setCreateText] = useState('')
  const [createIntent, setCreateIntent] = useState('')
  const [createKey, setCreateKey] = useState({ fingerprint: '', key: '' })
  const intents = usePoll(api.intents, 8000)
  const generation = useRef(0)
  const active = useRef(true)
  // Busy is "how many requests are in flight", not a boolean owned by whichever
  // request started last: a cancelled (out-of-order) response must still release
  // the panel, otherwise one quick click on a row disables every button.
  const pending = useRef(0)
  const begin = () => { pending.current++; setBusy(true) }
  const end = () => {
    pending.current = Math.max(0, pending.current - 1)
    if (pending.current === 0) setBusy(false)
  }

  useEffect(() => {
    active.current = true
    void load()
    return () => { active.current = false; generation.current++ }
    // The filter is the query; load() closes over it for this mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function load(cursor = '', nextFilter: InstructionFilter = filter) {
    const request = ++generation.current
    begin(); setError('')
    try {
      const next = await api.instructions(cursor, nextFilter)
      if (!active.current || request !== generation.current) return
      setPage(old => cursor && old ? {...next, instructions: [...old.instructions, ...next.instructions]} : next)
    } catch (e) { if (active.current && request === generation.current) setError(String(e)) }
    finally { if (active.current) end() }
  }

  function chooseFilter(next: InstructionFilter) {
    if (busy || next === filter) return
    setFilter(next)
    setPage(undefined)
    setSelected(undefined)
    void load('', next)
  }

  /** Open one existing instruction for correction/history. */
  function edit(row: Instruction) {
    generation.current++
    setCreating(false)
    setSelected(row); setDraft(row.text); setHistory(undefined); setError('')
  }

  function startCreate() {
    generation.current++
    setSelected(undefined); setHistory(undefined); setError('')
    setCreateText(''); setCreateIntent(''); setCreateKey({ fingerprint: '', key: '' })
    setCreating(true)
  }

  async function save(state: Instruction['state'] = 'active') {
    if (!selected || busy || !draft.trim()) return
    begin(); setError('')
    const request = ++generation.current
    try {
      const updated = await api.updateInstruction(selected.id, {version: selected.version, text: draft.trim(), state})
      if (!active.current || request !== generation.current) return
      if (state === 'revoked') {
        // Revoking drops the row out of the effective set. Show it as 已撤销
        // instead of letting it vanish: the receipt is the row itself, and the
        // editor keeps a 恢复并保存 affordance for an accidental click.
        setFilter('all')
        setSelected(updated); setDraft(updated.text); setHistory(undefined)
        await load('', 'all')
        onChanged?.()
        return
      }
      setSelected(undefined); setDraft(''); setHistory(undefined)
      await load()
      onChanged?.()
    } catch (e) { if (active.current && request === generation.current) setError(instructionErrorMessage(e)) }
    finally { if (active.current) end() }
  }

  async function create() {
    const text = createText.trim()
    if (busy || !text) return
    const keyed = directiveKeyFor(createKey, createInstructionFingerprint({ text, intentID: createIntent }), randomRequestKey)
    setCreateKey(keyed)
    begin(); setError('')
    const request = ++generation.current
    try {
      const result = await api.createInstruction(
        createInstructionRequest({ text, intentID: createIntent }, keyed.key),
      )
      if (!active.current || request !== generation.current) return
      setCreating(false); setCreateText(''); setCreateIntent('')
      setCreateKey({ fingerprint: '', key: '' })
      await load()
      setSelected(result.instruction); setDraft(result.instruction.text)
      onChanged?.()
    } catch (e) { if (active.current && request === generation.current) setError(instructionErrorMessage(e)) }
    finally { if (active.current) end() }
  }

  async function readHistory(cursor = '') {
    if (!selected || busy) return
    const request = ++generation.current
    begin(); setError('')
    try {
      const next = await api.instructionHistory(selected.id, cursor)
      if (!active.current || request !== generation.current) return
      setHistory(old => cursor && old ? {...next, history: [...old.history, ...next.history]} : next)
    } catch (e) { if (active.current && request === generation.current) setError(instructionErrorMessage(e)) }
    finally { if (active.current) end() }
  }

  const rows = sortInstructions(page?.instructions ?? [])
  const loaded = page?.instructions.length ?? 0
  const directions = intents.data ?? []

  return (
    <div className="flex h-full min-h-0 flex-col" data-instructions-panel>
      <div className="shrink-0 border-b border-hairline px-3 py-2 text-[12px] leading-5 text-ink-2">
        这里列出全部持久指令，也是唯一的管理入口：新增、更正、撤销都在这里。改口请直接更正原条目（保存后是新版本，旧正文不再进入提示词），不要让两条相反的指令同时生效。
      </div>

      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline px-3 py-2">
        <Button size="sm" disabled={busy} onClick={startCreate}>
          <Plus className="h-3.5 w-3.5" />
          新增指令
        </Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => void load()}>刷新列表</Button>
        <ToggleGroup value={filter} onValueChange={(value) => chooseFilter(value === 'all' ? 'all' : 'active')} ariaLabel="持久指令范围">
          <ToggleGroupItem value="active" disabled={busy} className="h-8 px-2 text-[11px]">仅有效</ToggleGroupItem>
          <ToggleGroupItem value="all" disabled={busy} className="h-8 px-2 text-[11px]">含已撤销</ToggleGroupItem>
        </ToggleGroup>
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
              title={filter === 'active' ? '没有生效中的持久指令' : '还没有任何持久指令'}
              description="点上方「新增指令」建立第一条；一次性提示不要写在这里，它只影响一轮。"
            />
          )}
          <div className="space-y-1">
            {rows.map(row => (
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
                  <span className="truncate">{instructionScopeLabel(row)}</span>
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
          {creating ? (
            <div className="space-y-2.5">
              <div className="flex flex-wrap items-center gap-2">
                <span className="tw-section-title">新增指令</span>
                <span className="text-[11px] text-ink-3">只在确实需要追加一条独立约束时使用</span>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <ToggleGroup
                  value={createIntent ? 'intent' : 'project'}
                  onValueChange={(value) => setCreateIntent(value === 'intent' ? (directions[0]?.id ?? '') : '')}
                  ariaLabel="指令作用域"
                >
                  <ToggleGroupItem value="project" disabled={busy} className="h-8 px-2 text-[11px]">整个项目</ToggleGroupItem>
                  <ToggleGroupItem value="intent" disabled={busy || directions.length === 0} className="h-8 px-2 text-[11px]">某个方向</ToggleGroupItem>
                </ToggleGroup>
              </div>
              {createIntent ? (
                <Select value={createIntent || undefined} onValueChange={setCreateIntent} disabled={busy}>
                  <SelectTrigger className="h-8 text-[12px]" aria-label="选择指令作用方向">
                    <SelectValue placeholder="选择方向" />
                  </SelectTrigger>
                  <SelectContent className="max-w-[calc(100vw-2rem)]">
                    {directions.map((intent) => (
                      <SelectItem key={intent.id} value={intent.id} className="text-[12px]">
                        <span className="truncate">{intent.title}</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <div className="tw-inset rounded-md px-2.5 py-2 text-[11px] leading-4 text-ink-3">
                  适用范围（建立后不可改）：整个项目，之后新建的所有方向都继承。
                </div>
              )}
              <Textarea
                aria-label="新指令正文"
                rows={6}
                maxLength={4000}
                disabled={busy}
                value={createText}
                onChange={e => setCreateText(e.target.value)}
                placeholder="写当前状态口径：允许什么、禁止什么、边界在哪。不要写步骤顺序——那属于意图或一次性提示。"
                className="text-[12px] leading-relaxed"
              />
              <div className="flex flex-wrap items-center gap-2">
                <Button size="sm" disabled={busy || !createText.trim()} onClick={() => void create()}>
                  {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                  创建指令
                </Button>
                <Button size="sm" variant="ghost" disabled={busy} onClick={() => setCreating(false)}>取消</Button>
              </div>
            </div>
          ) : !selected ? (
            <div className="space-y-2">
              <p className="text-[12px] leading-relaxed text-ink-2">
                在左侧选择一条指令查看或更正；需要追加一条独立约束时点「新增指令」。同一主题的改口请直接更正原条目。
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
                适用范围（只读）：{instructionScopeLabel(selected)}
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
              <p className="text-[11px] leading-4 text-ink-3">修改基于打开时的版本：并发更正会提示冲突，刷新列表后重新打开再保存。</p>
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
