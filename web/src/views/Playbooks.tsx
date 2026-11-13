import { useEffect, useMemo, useRef, useState } from 'react'
import { Archive, Ban, BookMarked, Check, Copy, RefreshCw, Search, ShieldCheck, Sparkles, Trash2, Globe } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Chip,
  EmptyState,
  Input,
  SkeletonList,
  useConfirm,
} from '@cyber/ui'
import { Page } from '@/components/Page'
import { api, projectID } from '@/lib/api'
import { fmtTime } from '@/lib/format'
import { useDebounced, useLoad } from '@/lib/hooks'
import type { Playbook, PlaybookStatus } from '@/lib/types'

// The project's knowledge table, not its findings (D70). Two writers, two
// tiers: the executor that ran the tactic writes a confirmed entry, the
// commander can only nominate a candidate; recall in the dispatch brief marks
// the difference, and this page is where a human sees and curates it.
const STATUS: Record<
  string,
  { label: string; variant: 'success' | 'warning' | 'muted'; hint: string }
> = {
  confirmed: { label: '已确认', variant: 'success', hint: '执行者在跑过的那一轮里声明' },
  candidate: { label: '候选', variant: 'warning', hint: '指挥官提名，还没有任何一轮执行确认过' },
  retired: { label: '已退役', variant: 'muted', hint: '不再进入派发简报；文本保留' },
}

const STATUS_ORDER: PlaybookStatus[] = ['confirmed', 'candidate', 'retired']

function statusMeta(status: string) {
  return STATUS[status] ?? { label: status || '未知', variant: 'muted' as const, hint: '' }
}

function outcomeMeta(outcome?: string) {
  if (outcome === 'dead_end') return { label: '死路', variant: 'caution' as const, icon: Ban }
  return { label: '可用', variant: 'success' as const, icon: ShieldCheck }
}

function originLabel(origin: string) {
  return origin === 'executor' ? '执行者声明' : '指挥官提名'
}

export function Playbooks() {
  const confirm = useConfirm()

  const [q, setQ] = useState('')
  const query = useDebounced(q, 150)
  const [status, setStatus] = useState<PlaybookStatus | 'all'>('all')
  const [tag, setTag] = useState<string | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const [copied, setCopied] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [archived, setArchived] = useState(false)
  const params = { q: query, status: status === 'all' ? '' : status, tag: tag ?? '', archived: archived ? 'only' : 'exclude' }
  const list = useLoad(() => api.playbooksPage(params), [query, status, tag, archived])
  const [extra, setExtra] = useState<Playbook[]>([])
  const [nextCursor, setNextCursor] = useState('')
  const generation = useRef(0)
  useEffect(() => {
    generation.current++
    setBusy('')
    setExtra([])
    setNextCursor(list.data?.next_cursor ?? '')
    return () => { generation.current++ }
  }, [list.data, query, status, tag, archived])
  const entries = useMemo(() => [...(list.data?.playbooks ?? []), ...extra], [list.data, extra])
  const [history, setHistory] = useState<{
    id: string
    rows: { version: number; operation: string; at: string; value?: Playbook }[]
    next?: string
  }>()

  const counts = useMemo(() => {
    if (list.data?.counts) return list.data.counts
    const out: Record<string, number> = {}
    for (const p of entries) out[p.status] = (out[p.status] ?? 0) + 1
    return out
  }, [entries, list.data])

  const tags = useMemo(() => {
    const out = new Map<string, number>()
    for (const p of entries) for (const t of p.tags ?? []) out.set(t, (out.get(t) ?? 0) + 1)
    return [...out.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  }, [entries])

  const filtered = entries

  const current = filtered.find((p) => p.id === selected) ?? filtered[0]

  async function more() {
    if (!nextCursor || busy || list.loading || list.refreshing) return
    const request = generation.current
    setBusy('page')
    setError('')
    try {
      const page = await api.playbooksPage({ ...params, cursor: nextCursor })
      if (request !== generation.current) return
      setExtra(old => [...old, ...page.playbooks])
      setNextCursor(page.next_cursor ?? '')
    } catch (e) {
      if (request === generation.current) setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (request === generation.current) setBusy('')
    }
  }

  async function archive(p: Playbook) {
    const request = generation.current
    setBusy(p.id)
    setError('')
    try {
      await api.archivePlaybook(p, !p.archived)
      if (request === generation.current) list.reload()
    } catch (e) {
      if (request === generation.current) setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (request === generation.current) setBusy('')
    }
  }

  async function readHistory(p: Playbook, cursor = '') {
    const request = generation.current
    setBusy(p.id)
    setError('')
    try {
      const page = await api.playbookHistory(p.id, cursor)
      if (request !== generation.current) return
      setHistory(old => ({
        id: p.id,
        rows: cursor && old?.id === p.id ? [...old.rows, ...page.history] : page.history,
        next: page.next_cursor,
      }))
    } catch (e) {
      if (request === generation.current) setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (request === generation.current) setBusy('')
    }
  }

  async function copy(text: string, key: string) {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(key)
      window.setTimeout(() => setCopied((v) => (v === key ? '' : v)), 1200)
    } catch {
      /* clipboard is best-effort; the text is visible anyway */
    }
  }

  async function retire(p: Playbook) {
    const ok = await confirm({
      title: '退役这条战场知识？',
      description: `「${p.title}」不再进入执行者的派发简报。文本会保留，之后执行者同名声明可以让它复活。`,
      confirmLabel: '退役',
      destructive: true,
    })
    if (!ok) return
    setBusy(p.id)
    setError('')
    try {
      await api.retirePlaybook(p.id)
      list.reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  async function remove(p: Playbook) {
    if (!await confirm({title:'删除这条战场知识？',description:`删除「${p.title}」的记录；来源事实和已发布的世界知识保留。`,confirmLabel:'删除',destructive:true})) return
    setBusy(p.id); setError('')
    try { await api.deletePlaybook(p.id,p.updated_at); list.reload() }
    catch(e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy('') }
  }

  return (
    <Page
      title="战场知识"
      subtitle={`${archived ? '归档' : '当前'}知识 · 已载 ${entries.length}/${list.data?.total ?? 0} 条 · 已确认 ${counts.confirmed ?? 0} · 候选 ${counts.candidate ?? 0}`}
      actions={
        <div className="flex flex-wrap gap-2">
          <Button size="xs" variant="outline" onClick={() => setArchived(v => !v)}>{archived ? '查看当前' : '查看归档'}</Button>
          <Button size="xs" variant="outline" onClick={() => void more()} disabled={!nextCursor || !!busy || list.loading || list.refreshing}>加载更多</Button>
          <Button size="xs" variant="outline" onClick={list.reload} disabled={list.loading || list.refreshing}>
            <RefreshCw className={list.loading || list.refreshing ? 'h-3 w-3 animate-spin' : 'h-3 w-3'} />
            刷新
          </Button>
        </div>
      }
      scroll={false}
    >
      <div className="flex h-full min-h-0 flex-col overflow-y-auto md:flex-row md:overflow-hidden">
        <aside className="flex h-[min(42vh,22rem)] min-h-[280px] w-full shrink-0 flex-col border-b border-border md:h-auto md:min-h-0 md:w-[380px] md:border-b-0 md:border-r">
          <div className="shrink-0 space-y-2 border-b border-border p-3">
            <div className="relative">
              <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={q}
                onChange={(e) => setQ(e.target.value)}
                placeholder="搜索名称、何时用、命令、标签"
                className="h-8 pl-7 text-[12px]"
              />
            </div>
            <div className="flex flex-wrap gap-1">
              <Chip
                active={status === 'all'}
                onClick={() => setStatus('all')}
                className="px-1.5 py-0 text-[11px]"
              >
                全部
                <span className="font-mono opacity-60">{Object.values(counts).reduce((sum, count) => sum + count, 0)}</span>
              </Chip>
              {STATUS_ORDER.map((s) => (
                <Chip
                  key={s}
                  active={status === s}
                  onClick={() => setStatus(status === s ? 'all' : s)}
                  className="px-1.5 py-0 text-[11px]"
                  title={STATUS[s]?.hint}
                >
                  {STATUS[s]?.label ?? s}
                  <span className="font-mono opacity-60">{counts[s] ?? 0}</span>
                </Chip>
              ))}
            </div>
            {tags.length > 0 && (
              <div className="flex max-h-[76px] flex-wrap gap-1 overflow-y-auto" title="已载入条目的标签">
                {tags.map(([name, n]) => (
                  <Chip
                    key={name}
                    active={tag === name}
                    onClick={() => setTag(tag === name ? null : name)}
                    className="px-1.5 py-0 text-[11px]"
                    title={tag === name ? `取消 ${name} 过滤` : `只看 ${name}`}
                  >
                    {name}
                    <span className="font-mono opacity-60">{n}</span>
                  </Chip>
                ))}
              </div>
            )}
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto p-1.5">
            {list.loading && <SkeletonList count={6} />}
            {list.error && <Callout tone="destructive">{list.error}</Callout>}
            {!list.loading && !list.error && entries.length === 0 && (query || tag || status !== 'all') && (
              <div className="px-2 py-6 text-[12px] text-muted-foreground">
                没有匹配的战场知识。换个关键词，或点掉状态 / 标签过滤。
              </div>
            )}
            {!list.loading && !list.error && entries.length === 0 && !query && !tag && status === 'all' && (
              <div className="px-2 py-6 text-[12px] leading-relaxed text-muted-foreground">
                {archived ? '没有归档的战场知识。' : '还没有战场知识。执行者可在运行后声明，指挥官也可以提名候选。'}
              </div>
            )}
            {filtered.map((p) => {
              const st = statusMeta(p.status)
              return (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => setSelected(p.id)}
                  className={
                    'mb-0.5 block w-full rounded-md px-2.5 py-2 text-left transition-colors ' +
                    (current?.id === p.id ? 'bg-primary/10' : 'hover:bg-muted/60')
                  }
                >
                  <div className="flex items-center gap-2">
                    <span className="truncate text-[13px] font-medium text-foreground">{p.title}</span>
                    <Badge size="sm" variant={st.variant} className="ml-auto shrink-0">
                      {st.label}
                    </Badge>
                  </div>
                  <div className="mt-0.5 line-clamp-2 text-[11.5px] leading-relaxed text-muted-foreground">
                    {p.when}
                  </div>
                  {(p.tags ?? []).length > 0 && (
                    <div className="mt-1 flex flex-wrap gap-1">
                      {(p.tags ?? []).slice(0, 4).map((t) => (
                        <Badge key={t} size="sm" variant="muted">
                          {t}
                        </Badge>
                      ))}
                    </div>
                  )}
                </button>
              )
            })}
          </div>

          <div className="shrink-0 border-t border-border px-3 py-2 text-[11px] text-muted-foreground">
            已载入 {entries.length} / {list.data?.total ?? 0} 条
            {tag ? ` · 标签 ${tag}` : ''}
          </div>
        </aside>

        <section className="min-h-[280px] min-w-0 flex-1 overflow-y-auto md:min-h-0">
          {!current ? (
            <div className="flex h-full items-center justify-center">
              <EmptyState
                icon={BookMarked}
                title={entries.length === 0 ? '还没有战场知识' : '没有匹配项'}
                description={
                  entries.length === 0
                    ? '执行者在跑过的那一轮里写一条 kind=playbook；指挥官只能提名候选。候选被执行者同名声明后升级为确认。'
                    : '换个关键词或状态试试。'
                }
              />
            </div>
          ) : (
            <article className="mx-auto max-w-3xl px-6 py-5">
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="text-h4 font-semibold text-foreground">{current.title}</h2>
                <Badge variant={statusMeta(current.status).variant}>
                  {statusMeta(current.status).label}
                </Badge>
                {(() => {
                  const o = outcomeMeta(current.outcome)
                  const Icon = o.icon
                  return (
                    <Badge variant={o.variant} className="gap-1">
                      <Icon className="h-3 w-3" />
                      {o.label}
                    </Badge>
                  )
                })()}
              </div>

              <p className="mt-1.5 text-[13px] leading-6 text-muted-foreground">{current.when}</p>

              <div className="mt-2 flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                <span>{originLabel(current.origin)}</span>
                {current.intent_id && <span className="font-mono">· {current.intent_id}</span>}
                <span>· 更新于 {fmtTime(current.updated_at)}</span>
                <span className="font-mono">· {current.id}</span>
              </div>

              {current.status === 'candidate' && (
                <Callout tone="warning" className="mt-3">
                  候选只是指挥官从既有事实里看出的模式，没有执行证据。它会在派发简报里标“候选·未验证”，
                  用前先按现场核对。
                </Callout>
              )}
              {current.outcome === 'dead_end' && (
                <Callout tone="info" className="mt-3">
                  这条路走不通。除非前提（版本、入口、权限）变了，不要原样重试。
                </Callout>
              )}

              {current.doc && (
                <div className="mt-5">
                  <div className="mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                    命令 / 判据 / 坑
                  </div>
                  <div className="relative">
                    <pre className="overflow-x-auto whitespace-pre-wrap break-words rounded-md border border-border bg-muted/30 px-2.5 py-2 pr-16 font-mono text-[11.5px] leading-relaxed text-foreground/90">
                      {current.doc}
                    </pre>
                    <Button
                      size="xs"
                      variant="ghost"
                      className="absolute right-1 top-1 gap-1 text-[11px]"
                      onClick={() => copy(current.doc ?? '', current.id)}
                      title="复制战场知识"
                    >
                      {copied === current.id ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
                      {copied === current.id ? '已复制' : '复制'}
                    </Button>
                  </div>
                </div>
              )}

              {(current.tags ?? []).length > 0 && (
                <div className="mt-4 flex flex-wrap gap-1.5">
                  {(current.tags ?? []).map((t) => (
                    <Chip
                      key={t}
                      active={tag === t}
                      onClick={() => setTag(tag === t ? null : t)}
                      className="px-1.5 py-0 text-[11px]"
                    >
                      {t}
                    </Chip>
                  ))}
                </div>
              )}

              {(current.from_fact_ids ?? []).length > 0 && (
                <div className="mt-4">
                  <div className="mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                    来源事实
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {(current.from_fact_ids ?? []).map((id) => (
                      <Badge key={id} size="sm" variant="muted" className="font-mono">
                        {id}{current.from_fact_versions?.[id] ? ` · v${current.from_fact_versions[id]}` : ''}
                      </Badge>
                    ))}
                  </div>
                </div>
              )}

              {current.source_run_id && (
                <div className="mt-3 text-[11px] text-muted-foreground">
                  最近声明运行：<a className="font-mono underline" href={`?view=runs&run=${encodeURIComponent(current.source_run_id)}`}>{current.source_run_id}</a>
                </div>
              )}

              {error && (
                <Callout tone="destructive" className="mt-4">
                  {error}
                </Callout>
              )}

              <footer className="mt-6 flex flex-wrap items-center gap-3 border-t border-border pt-3 text-[11px] text-muted-foreground">
                <span>项目私有知识：不是事实、不进报告、不算覆盖。</span>
                <span className="ml-auto flex items-center gap-1.5">
                  {current.status === 'confirmed' && <Sparkles className="h-3 w-3" />}
                  {current.status === 'retired' && <Archive className="h-3 w-3" />}
                </span>
                <Button size="xs" variant="outline" disabled={!!busy} onClick={() => window.location.assign(`/g/?view=kb&source_project=${encodeURIComponent(projectID())}&source_playbook=${encodeURIComponent(current.id)}`)}><Globe className="h-3 w-3" />提名世界知识</Button>
                <Button size="xs" variant="ghost" disabled={!!busy} onClick={() => remove(current)}><Trash2 className="h-3 w-3" />删除</Button>
                <Button size="xs" variant="outline" disabled={!!busy} onClick={() => void archive(current)}>{current.archived ? '恢复' : '归档'}</Button>
                <Button size="xs" variant="ghost" disabled={!!busy} onClick={() => void readHistory(current)}>修订历史</Button>
                {current.status !== 'retired' && (
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={busy === current.id}
                    onClick={() => retire(current)}
                  >
                    {busy === current.id ? '退役中…' : '退役'}
                  </Button>
                )}
              </footer>
              {history?.id === current.id && (
                <div className="mt-3 space-y-2 text-[11px]">
                  {history.rows.map((h, i) => (
                    <details key={`${h.version}:${i}`}>
                      <summary>v{h.version} · {h.operation} · {fmtTime(h.at)}</summary>
                      <pre className="whitespace-pre-wrap break-words">{JSON.stringify(h.value, null, 2)}</pre>
                    </details>
                  ))}
                  {history.next && <Button size="xs" variant="ghost" disabled={!!busy} onClick={() => void readHistory(current, history.next)}>继续读取历史</Button>}
                </div>
              )}
            </article>
          )}
        </section>
      </div>
    </Page>
  )
}
