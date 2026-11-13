// Explicit reading context for one direction: references, pins and interest
// entities. This is context, not a prerequisite — editing it never changes the
// hard from_fact_ids that gate dispatch.
//
// Concurrency is the point: the editor opens the row, keeps the context_version
// it displayed, and sends that version with the whole selection. A stale edit
// answers 409 and is surfaced as "已被他人修改，刷新后重试" (the draft is kept,
// never silently overwritten); a foreign participant answers 403. Polling keeps
// the panel fresh without discarding an unsaved draft.
import { useEffect, useState } from 'react'
import { Badge, Button, Callout, Input, Textarea } from '@cyber/ui'
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'
import {
  contextErrorMessage,
  contextErrorKind,
  contextFactLabel,
  filterContextFacts,
  formatEntityRefs,
  intentContextSelection,
  parseEntityRefs,
  putIntentContext,
  sameContextSelection,
  type ContextErrorKind,
  type IntentContextSelection,
} from '@/lib/context'
import type { Fact, Intent } from '@/lib/types'

export function IntentContextEditor({
  intent,
  onSaved,
  onSelectFact,
}: {
  intent: Intent
  /** Reload the ledger after a successful save (the graph refreshes its model). */
  onSaved: () => void
  /** Optional: select a fact node in the graph when a cited fact is clicked. */
  onSelectFact?: (factID: string) => void
}) {
  const [open, setOpen] = useState(false)
  const version = intent.context_version ?? 0
  return (
    <div className="rounded-lg border border-hairline bg-surface-2/40 px-2.5 py-2" data-intent-context-editor>
      <div className="flex items-center gap-1.5">
        <span className="tw-eyebrow normal-case tracking-normal text-ink-2">上下文（引用/固定）</span>
        <Badge size="sm" variant="muted" title="context_version：每次成功修改 +1；保存时带上打开时的版本">
          上下文 v{version}
        </Badge>
        <Button size="xs" variant="ghost" className="ml-auto" onClick={() => setOpen((value) => !value)}>
          {open ? '收起' : '编辑'}
        </Button>
      </div>
      <div className="mt-1 text-[11px] leading-relaxed text-ink-3">
        引用 <span className="tw-nums font-mono text-ink-2">{intent.context_fact_ids?.length ?? 0}</span> · 固定{' '}
        <span className="tw-nums font-mono text-ink-2">{intent.pinned_fact_ids?.length ?? 0}</span> · 实体{' '}
        <span className="tw-nums font-mono text-ink-2">{intent.interest_entity_refs?.length ?? 0}</span>
        。引用/固定只决定这次读什么，不改硬前提。
      </div>
      {open && <ContextEditorBody key={intent.id} intent={intent} onSaved={onSaved} onSelectFact={onSelectFact} />}
    </div>
  )
}

function ContextEditorBody({
  intent,
  onSaved,
  onSelectFact,
}: {
  intent: Intent
  onSaved: () => void
  onSelectFact?: (factID: string) => void
}) {
  const [base, setBase] = useState<IntentContextSelection>(() => intentContextSelection(intent))
  const [references, setReferences] = useState<string[]>(() => intentContextSelection(intent).references)
  const [pins, setPins] = useState<string[]>(() => intentContextSelection(intent).pins)
  const [entitiesText, setEntitiesText] = useState<string>(() =>
    formatEntityRefs(intentContextSelection(intent).entities),
  )
  const [picker, setPicker] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [errorKind, setErrorKind] = useState<ContextErrorKind | ''>('')
  const [note, setNote] = useState('')
  const [remoteChanged, setRemoteChanged] = useState(false)

  // The picker searches the project's facts by id/title. The page is bounded
  // by the server (500 is its maximum), so an old fact id can still be added by
  // typing it and will be validated by the store on save.
  const facts = useLoad(() => api.facts({ limit: 500 }), [])
  const factRows: Fact[] = facts.data ?? []

  const server = intentContextSelection(intent)
  const draft: IntentContextSelection = {
    version: base.version,
    references,
    pins,
    entities: parseEntityRefs(entitiesText),
  }
  const dirty = !sameContextSelection(draft, base)
  const matches = filterContextFacts(factRows, picker, 8)

  function adopt(next: IntentContextSelection) {
    setBase(next)
    setReferences(next.references)
    setPins(next.pins)
    setEntitiesText(formatEntityRefs(next.entities))
    setRemoteChanged(false)
  }

  // Polling must never discard an unsaved draft. A clean draft simply follows
  // the ledger; a dirty draft stays, and a newer server version raises the
  // conflict hint up front instead of letting Save fail silently.
  useEffect(() => {
    // A row older than the version the editor already adopted is a stale poll
    // response, not a remote change: "load latest" must be able to move the
    // baseline forward without a second conflict hint for the version it just
    // read.
    if (server.version < base.version) return
    if (sameContextSelection(server, base)) {
      // A no-op edit or a change reverted by another editor can advance the
      // CAS token without changing these fields. Keep the local draft, but
      // save it against the latest identical baseline.
      if (server.version > base.version) {
        setBase(server)
        setRemoteChanged(false)
      }
      return
    }
    if (!dirty) {
      adopt(server)
      return
    }
    setRemoteChanged(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intent, base, references, pins, entitiesText])

  async function save() {
    if (busy || !dirty) return
    setBusy(true)
    setError('')
    setErrorKind('')
    setNote('')
    try {
      const updated = await putIntentContext(intent.id, {
        version: base.version,
        references,
        pins,
        entities: parseEntityRefs(entitiesText),
      })
      const next = intentContextSelection(updated)
      adopt(next)
      setNote(`已保存，上下文版本 v${next.version}`)
      onSaved()
    } catch (cause) {
      const kind = contextErrorKind(cause)
      setErrorKind(kind)
      setError(contextErrorMessage(cause))
      if (kind === 'conflict') setRemoteChanged(true)
    } finally {
      setBusy(false)
    }
  }

  // Explicit "读取最新": keeps the operator's draft, moves the version it will
  // save against. The next save is then a normal update instead of a 409.
  async function loadLatest() {
    setBusy(true)
    setError('')
    setErrorKind('')
    setNote('')
    try {
      const { intent: fresh } = await api.intent(intent.id)
      const next = intentContextSelection(fresh)
      setBase(next)
      setRemoteChanged(false)
      setNote(`已载入最新上下文版本 v${next.version}；草稿保留，保存会以该版本为基准`)
      // Deliberately no onSaved() here. A shell reload unmounts the node panel
      // (the graph snapshot is cleared while refetching), which would destroy
      // the draft this button promises to keep. The shell's normal poll brings
      // the refreshed row forward; until then the version guard above ignores
      // the stale row instead of re-raising the conflict hint.
    } catch (cause) {
      setErrorKind(contextErrorKind(cause))
      setError(contextErrorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  function addReference(id: string) {
    setReferences((prev) => (prev.includes(id) ? prev : [...prev, id]))
  }

  function addPin(id: string) {
    setPins((prev) => (prev.includes(id) ? prev : [...prev, id]))
  }

  return (
    <div className="mt-2 space-y-2.5 border-t border-hairline pt-2.5">
      <div className="text-[11px] leading-relaxed text-ink-3">
        操作员可改整个项目；参与者的运行只能改自己持有的方向，边界外请求会被服务端拒绝。
      </div>

      {remoteChanged && (
        <Callout tone="warning">
          上下文已被他人修改（服务端 v{server.version}）。保存会提示冲突，不会静默覆盖。
          <div className="mt-1.5">
            <Button size="sm" variant="outline" disabled={busy} onClick={() => void loadLatest()}>
              载入最新版本（保留草稿）
            </Button>
          </div>
        </Callout>
      )}

      <ContextList
        title={`引用（${references.length}）`}
        hint="这次想读的事实；不是派发门槛"
        ids={references}
        facts={factRows}
        onSelectFact={onSelectFact}
        onRemove={(id) => setReferences((prev) => prev.filter((value) => value !== id))}
      />
      <ContextList
        title={`固定（${pins.length}）`}
        hint="操作员/指挥官希望优先出现在焦点里的上下文"
        ids={pins}
        facts={factRows}
        onSelectFact={onSelectFact}
        onRemove={(id) => setPins((prev) => prev.filter((value) => value !== id))}
      />

      <div>
        <div className="mb-1 flex items-center gap-1.5">
          <span className="text-[12px] font-medium text-ink-2">添加引用/固定</span>
          <span className="tw-nums ml-auto font-mono text-[11px] text-ink-3">
            已载入 {factRows.length} 条事实
          </span>
        </div>
        <div className="flex gap-1.5">
          <Input
            value={picker}
            onChange={(event) => setPicker(event.target.value)}
            placeholder="按事实 id 或标题搜索"
            aria-label="搜索事实"
            className="h-8 flex-1 text-[12px]"
          />
          <Button size="xs" variant="ghost" disabled={facts.loading} onClick={() => facts.reload()}>
            刷新
          </Button>
        </div>
        {facts.error && <div className="mt-1 text-[11px] text-destructive">{facts.error}</div>}
        <div className="mt-1.5 space-y-1">
          {facts.loading && <div className="text-[11px] text-ink-3">正在载入事实…</div>}
          {!facts.loading && matches.length === 0 && (
            <div className="rounded-lg border border-dashed border-hairline px-2.5 py-2 text-[11px] leading-relaxed text-ink-3">
              没有匹配；也可以直接点下方已有条目，或输入完整事实 id 后由服务端校验。
            </div>
          )}
          {matches.map((fact) => (
            <div
              key={fact.id}
              className="flex items-center gap-1.5 rounded-lg border border-hairline bg-card/40 px-2 py-1 transition-colors hover:bg-surface-2/60"
            >
              <span className="tw-break shrink-0 font-mono text-[11px] text-ink-3">{fact.id}</span>
              <span className="min-w-0 flex-1 truncate text-[11px] text-ink-2" title={fact.title}>
                {fact.title}
              </span>
              <Button size="xs" variant="ghost" disabled={busy} onClick={() => addReference(fact.id)}>
                +引用
              </Button>
              <Button size="xs" variant="ghost" disabled={busy} onClick={() => addPin(fact.id)}>
                +固定
              </Button>
            </div>
          ))}
        </div>
      </div>

      <div>
        <div className="mb-1 text-[12px] font-medium text-ink-2">
          关注实体（{parseEntityRefs(entitiesText).length}）
        </div>
        <Textarea
          aria-label="关注实体"
          rows={2}
          value={entitiesText}
          onChange={(event) => setEntitiesText(event.target.value)}
          placeholder={'每行一个，例如 host:10.0.0.5\n只记录显式实体，不从标题推断'}
          className="text-[12px]"
        />
      </div>

      <HardPremises ids={intent.from_fact_ids ?? []} facts={factRows} onSelectFact={onSelectFact} />

      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" disabled={busy || !dirty} onClick={() => void save()}>
          {busy ? '保存中…' : '保存上下文'}
        </Button>
        <Button size="sm" variant="ghost" disabled={busy || !dirty} onClick={() => adopt(base)}>
          撤销修改
        </Button>
        <span className="tw-nums font-mono text-[11px] text-ink-3">将以 v{base.version} 保存</span>
        {dirty && (
          <Badge size="sm" variant="warning">
            未保存
          </Badge>
        )}
      </div>
      {error && (
        <Callout tone={errorKind === 'forbidden' ? 'destructive' : 'warning'}>
          {error}
          {errorKind === 'conflict' && (
            <div className="mt-1 text-[11px]">
              冲突意味着这个版本已经不是最新的：载入最新版本后重试，草稿不会丢。
            </div>
          )}
        </Callout>
      )}
      {note && <div className="text-[11px] text-ink-3">{note}</div>}
    </div>
  )
}

function ContextList({
  title,
  hint,
  ids,
  facts,
  onSelectFact,
  onRemove,
}: {
  title: string
  hint: string
  ids: string[]
  facts: Fact[]
  onSelectFact?: (factID: string) => void
  onRemove: (factID: string) => void
}) {
  return (
    <div>
      <div className="flex items-center gap-1.5" title={hint}>
        <span className="text-[12px] font-medium text-ink-2">{title}</span>
        <span className="truncate text-[11px] text-ink-3">{hint}</span>
      </div>
      {ids.length === 0 && <div className="mt-0.5 text-[11px] text-ink-3">没有</div>}
      <div className="mt-1 space-y-1">
        {ids.map((id) => (
          <div
            key={id}
            className="flex items-center gap-1.5 rounded-lg border border-hairline bg-card/40 px-2 py-1 transition-colors hover:bg-surface-2/60"
          >
            {onSelectFact ? (
              <button
                type="button"
                onClick={() => onSelectFact(id)}
                className="min-w-0 flex-1 truncate text-left text-[11px] text-ink-2 transition-colors hover:text-info focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                title={`${id} · 点击在图上看这条事实`}
              >
                {contextFactLabel(id, facts)}
              </button>
            ) : (
              <span className="min-w-0 flex-1 truncate text-[11px] text-ink-2" title={id}>
                {contextFactLabel(id, facts)}
              </span>
            )}
            <span className="tw-break shrink-0 font-mono text-[11px] text-ink-3">{id}</span>
            <Button
              size="xs"
              variant="ghost"
              className="shrink-0 px-1"
              aria-label={`移除 ${id}`}
              onClick={() => onRemove(id)}
            >
              ×
            </Button>
          </div>
        ))}
      </div>
    </div>
  )
}

function HardPremises({
  ids,
  facts,
  onSelectFact,
}: {
  ids: string[]
  facts: Fact[]
  onSelectFact?: (factID: string) => void
}) {
  return (
    <div className="rounded-lg border border-warning/25 bg-warning/[0.06] px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-[12px] font-medium text-warning">必要前提（from_fact_ids）</span>
        <Badge size="sm" variant="warning">
          硬门槛 · 只读
        </Badge>
      </div>
      {ids.length === 0 ? (
        <div className="mt-1 text-[11px] text-ink-3">没有硬前提：这条方向不依赖已有断言才能派发</div>
      ) : (
        <div className="mt-1 space-y-0.5">
          {ids.map((id) => (
            <button
              key={id}
              type="button"
              disabled={!onSelectFact}
              onClick={() => onSelectFact?.(id)}
              className="flex w-full items-center gap-1.5 rounded px-1 py-0.5 text-left transition-colors enabled:hover:bg-surface-2 disabled:cursor-default"
              title={id}
            >
              <span className="tw-break shrink-0 font-mono text-[11px] text-ink-3">{id}</span>
              <span className="min-w-0 flex-1 truncate text-[11px] text-ink-2">
                {contextFactLabel(id, facts)}
              </span>
            </button>
          ))}
        </div>
      )}
      <div className="mt-1 text-[11px] leading-relaxed text-ink-3">
        改这里不会改硬前提：引用/固定只是阅读选择，派发仍要求这些前提存在且未被否决。
      </div>
    </div>
  )
}
