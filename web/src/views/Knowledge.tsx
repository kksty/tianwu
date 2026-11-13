import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { BookOpen, Download, FilePlus2, RefreshCw, Search, Trash2, TriangleAlert, Upload } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Chip,
  EmptyState,
  Field,
  Input,
  ListRow,
  SkeletonList,
  Spinner,
  Tabs,
  TabsList,
  TabsTrigger,
  Textarea,
  useConfirm,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { useDebounced, useLoad } from '@/lib/hooks'
import type { KBCandidate, KBEntry, Playbook } from '@/lib/types'

const blank = (): KBEntry => ({id: '', title: '', description: '', detail: '', source: '', tags: [], source_refs: []})
const lines = (s: string) => s.split('\n').map(v => v.trim()).filter(Boolean)

export function Knowledge() {
  const confirm = useConfirm()
  const params = new URLSearchParams(window.location.search)
  const sourceProject = params.get('source_project') ?? ''
  const sourceID = params.get('source_playbook') ?? ''
  const source = useLoad(() => sourceProject && sourceID ? api.playbookSource(sourceProject,sourceID) : Promise.resolve<Playbook | null>(null), [sourceProject,sourceID])
  const sourceOpened = useRef(false)
  const list = useLoad(api.kb, [])
  const drafts = useLoad(api.kbCandidates, [])
  const [tab, setTab] = useState<'published' | 'candidates'>('published')
  const [q, setQ] = useState('')
  const query = useDebounced(q, 150)
  const [tag, setTag] = useState<string | null>(null)
  const [selected, setSelected] = useState('')
  const [editing, setEditing] = useState<{entry: KBEntry; reason: string; base_revision: string; id_locked: boolean; source?: {id: string; project: string; updated_at: string}; id?: string; version?: number} | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const entries = list.data?.entries ?? []
  const candidates = drafts.data ?? []
  const rows = tab === 'published' ? entries.map(entry => ({key: entry.id, entry, candidate: undefined as KBCandidate | undefined})) : candidates.map(candidate => ({key: candidate.id, entry: candidate.entry, candidate}))
  const filtered = rows.filter(row => (!tag || row.entry.tags?.includes(tag)) && JSON.stringify(row.entry).toLowerCase().includes(query.trim().toLowerCase()))
  const current = filtered.find(row => row.key === selected) ?? filtered[0]
  const tags = useMemo(() => [...new Set(entries.flatMap(e => e.tags ?? []))].sort(), [entries])
  // 选中的标签可能因条目被删除或重新加载而从 tags 消失：它不再渲染成可点的 chip，
  // 列表却会一直空着、刷新也救不回来。标签集合变化时清掉这个悬空筛选。
  useEffect(() => {
    if (tag && !tags.includes(tag)) setTag(null)
  }, [tag, tags])
  const refresh = () => { list.reload(); drafts.reload() }
  const pendingCandidates = candidates.filter(c => !c.published).length
  async function act(action: () => Promise<unknown>, message: string) {
    setBusy(true); setError(''); setNotice('')
    try { await action(); refresh(); setNotice(message) }
    catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy(false) }
  }
  function edit(entry: KBEntry, candidate?: KBCandidate, playbook?: Playbook) {
    sourceOpened.current = true
    setEditing({entry: {...entry}, reason: candidate?.reason ?? '', base_revision: candidate?.base_revision ?? list.data?.revisions?.[entry.id] ?? '', id_locked:!!candidate?.id || !!entry.id, source:playbook ? {id:playbook.id,project:sourceProject,updated_at:playbook.updated_at} : undefined, id: candidate?.id, version: candidate?.version}); setError(''); setNotice('')
  }
  useEffect(() => { if (source.data && !sourceOpened.current) edit({...blank(),title:source.data.title},undefined,source.data) }, [source.data])
  async function save(event: FormEvent) {
    event.preventDefault()
    if (!editing) return
    const body = {id:editing.id, version:editing.version, base_revision:editing.base_revision, reason:editing.reason, entry: {...editing.entry, tags: editing.entry.tags?.map(t => t.trim()).filter(Boolean), source_refs: lines((editing.entry.source_refs ?? []).join('\n'))}}
    await act(async () => { const row = !body.id && editing.source ? await api.nominatePlaybookKB(editing.source.project,editing.source.id,{...body,source_updated_at:editing.source.updated_at}) : await api.saveKBCandidate(body); setSelected(row.id); setTab('candidates'); setEditing(null) }, '候选已保存；由你点击发布后才会进入世界知识。')
  }
  async function remove() {
    if (!current) return
    const c = current.candidate
    if (!await confirm({title:c ? '删除这条候选？' : '删除这条世界知识？',description:c ? '删除候选记录，已发布的世界知识保留。' : '从当前实例的世界 KB 移除；内置条目会在此实例屏蔽。仓库 JSON 和已派发的文件快照不会自动修改。',confirmLabel:'删除',destructive:true})) return
    await act(() => c ? api.deleteKBCandidate(c.id,c.version) : api.deleteKB(current.entry.id,list.data?.revisions?.[current.entry.id] ?? ''),c ? '候选已删除。' : '知识已从当前实例移除；需要移除内置资产时，请导出全库并导入仓库。')
  }

  const errorCallout = (list.error || drafts.error) && (
    <div role="alert">
      <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <span className="min-w-0 tw-break">{list.error || drafts.error}</span>
          <Button size="xs" variant="outline" disabled={busy} onClick={refresh}>重试</Button>
        </div>
      </Callout>
    </div>
  )

  return (
    <Page
      title="世界知识"
      subtitle={`${entries.length} 条已发布 · ${pendingCandidates} 条待晋升 · 实例共享，按需检索`}
      actions={
        <>
          <Button size="xs" variant="outline" disabled={busy} onClick={() => act(api.reloadKB, '已重新加载磁盘条目，查询立即使用新版本。')}>
            <RefreshCw className="h-3.5 w-3.5" />重新加载
          </Button>
          <Button size="xs" variant="outline" asChild>
            <a href="/api/kb/export" download>
              <Download className="h-3.5 w-3.5" />导出内置包
            </a>
          </Button>
          {!editing && (
            <Button size="xs" disabled={busy} onClick={() => edit(blank())}>
              <FilePlus2 className="h-3.5 w-3.5" />新建候选
            </Button>
          )}
        </>
      }
      scroll={false}
    >
      <div className="flex h-full min-h-0 flex-col">
        {/* One filter row: category tabs, search, tag filters and the match
            count all at h-8 so the list starts high on the page. */}
        <div className="shrink-0 border-b border-hairline px-4 py-3 sm:px-5">
          <div className="flex flex-wrap items-center gap-2">
            <Tabs value={tab} onValueChange={(value) => { setTab(value as 'published' | 'candidates'); setSelected(''); setEditing(null) }}>
              <TabsList className="h-8">
                <TabsTrigger value="published" className="h-6 gap-1.5 px-2.5 text-[12px]" disabled={busy}>
                  已发布知识 <span className="tw-nums font-mono text-[11px] text-ink-3">{entries.length}</span>
                </TabsTrigger>
                <TabsTrigger value="candidates" className="h-6 gap-1.5 px-2.5 text-[12px]" disabled={busy}>
                  晋升候选 <span className="tw-nums font-mono text-[11px] text-ink-3">{pendingCandidates}</span>
                </TabsTrigger>
              </TabsList>
            </Tabs>
            <div className="relative min-w-[180px] flex-1">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" aria-hidden="true" />
              <Input
                aria-label="搜索知识"
                value={q}
                onChange={e => setQ(e.target.value)}
                placeholder="搜索知识、适用条件、来源"
                className="h-8 pl-8 text-[12px]"
              />
            </div>
            <span className="tw-nums shrink-0 font-mono text-[11px] text-ink-3">
              显示 {filtered.length} / {rows.length}
            </span>
            <Button size="sm" variant="ghost" disabled={busy} onClick={refresh}>刷新列表</Button>
          </div>
          {tags.length > 0 && (
            <div className="mt-2 flex max-h-16 flex-wrap gap-1 overflow-auto">
              {tag && (
                <Chip active onClick={() => setTag(null)} className="text-[11px]">
                  全部
                </Chip>
              )}
              {tags.map(t => (
                <Chip key={t} active={tag === t} onClick={() => setTag(tag === t ? null : t)} className="text-[11px]">
                  {t}
                </Chip>
              ))}
            </div>
          )}
        </div>

        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto md:flex-row md:overflow-hidden">
          <aside className="flex h-[min(42vh,22rem)] min-h-[280px] w-full shrink-0 flex-col border-b border-hairline md:h-auto md:min-h-0 md:w-[320px] md:border-b-0 md:border-r xl:w-[360px]">
            <div className="min-h-0 flex-1 overflow-auto p-2">
              {(list.loading || drafts.loading) && <SkeletonList count={5} />}
              {errorCallout}
              {filtered.map(row => {
                const active = current?.key === row.key
                return (
                  <ListRow
                    key={row.key}
                    active={active}
                    disabled={busy}
                    onClick={() => { setSelected(row.key); setEditing(null) }}
                    className="mb-1 min-h-[56px] gap-2.5 px-2.5 py-2.5"
                  >
                    <span className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-[13px] font-medium text-ink">{row.entry.title}</span>
                      {row.candidate && (
                        <Badge variant={row.candidate.published ? 'success' : 'warning'} size="sm" className="shrink-0">
                          {row.candidate.published ? '已发布' : '候选'}
                        </Badge>
                      )}
                    </span>
                    <span className="mt-1 line-clamp-2 text-[12px] leading-5 text-ink-2">{row.entry.description}</span>
                    <span className="mt-1 block truncate font-mono text-[11px] text-ink-3">{row.entry.id}</span>
                  </ListRow>
                )
              })}
              {!list.loading && !drafts.loading && filtered.length === 0 && (
                <EmptyState
                  compact
                  icon={BookOpen}
                  title={rows.length === 0 ? (tab === 'published' ? '还没有已发布知识' : '还没有晋升候选') : '没有匹配的条目'}
                  description={rows.length === 0
                    ? '晋升候选由运行沉淀，也可以在右上角「新建候选」手动补充事实与来源后发布。'
                    : '换个关键词，或清掉标签筛选。'}
                />
              )}
            </div>
            <div className="flex shrink-0 items-center gap-2 border-t border-hairline px-3 py-2 text-[11px] text-ink-3">
              <span className="tw-nums font-mono">显示 {filtered.length} / {rows.length}</span>
              <Button size="xs" variant="ghost" disabled={busy} onClick={refresh} className="ml-auto">刷新列表</Button>
            </div>
          </aside>

          <section className="min-h-[280px] min-w-0 flex-1 overflow-auto md:min-h-0">
            <div className="mx-auto max-w-3xl space-y-4 p-4 sm:p-6">
              {error && <div role="alert"><Callout tone="destructive">{error}</Callout></div>}
              {notice && <Callout tone="success">{notice}</Callout>}
              {source.error && (
                <div role="alert">
                  <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                      <span className="min-w-0 tw-break">来源战场知识读取失败：{source.error}</span>
                      <Button size="xs" variant="outline" onClick={source.reload}>重试</Button>
                    </div>
                  </Callout>
                </div>
              )}
              {source.data && (
                <details className="rounded-lg border border-hairline bg-surface-2/40 p-3 text-[12px] leading-6">
                  <summary className="cursor-pointer font-medium text-ink">来源战场知识：{source.data.title}（仅供编辑参考）</summary>
                  <p className="mt-2 text-ink-2">{source.data.when}</p>
                  <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words text-[12px] text-ink-2">{source.data.doc}</pre>
                  <p className="mt-2 text-ink-3">请人工提取通用事实、补齐来源和适用条件。原文不会自动进入世界 KB。</p>
                </details>
              )}

              {editing ? (
                <Panel title={editing.id ? '编辑晋升候选' : '提名世界知识'} extra={<Badge variant="outline">保存后仍是候选</Badge>}>
                  <form onSubmit={save}>
                    <fieldset disabled={busy} className="min-w-0 space-y-4">
                      <p className="text-[12px] leading-5 text-ink-3">
                        保留通用事实、适用条件与来源。项目凭据、临时环境状态和工具教程留在原处。
                      </p>
                      <div className="grid min-w-0 gap-4 sm:grid-cols-2">
                        <Field label="稳定 ID" hint="发布后以此 ID 引用；不可与已发布条目重复。">
                          <Input
                            required
                            disabled={editing.id_locked}
                            aria-label="稳定 ID"
                            value={editing.entry.id}
                            onChange={e => setEditing({...editing, base_revision:list.data?.deleted_revisions?.[e.target.value] ?? '', entry: {...editing.entry, id:e.target.value}})}
                            placeholder="例如 protocol-example"
                            className="font-mono text-[12px]"
                          />
                        </Field>
                        <Field label="标题">
                          <Input required aria-label="标题" value={editing.entry.title} onChange={e => setEditing({...editing, entry: {...editing.entry, title:e.target.value}})} />
                        </Field>
                      </div>
                      {!editing.id_locked && editing.base_revision && (
                        <Callout tone="info">此 ID 在当前实例已删除；显式发布后将重新收录。</Callout>
                      )}
                      {([['description', '摘要', 2], ['detail', '事实正文', 7], ['applicability', '适用范围与前提条件', 3], ['source', '来源说明', 2]] as const).map(([key, label, height]) => (
                        <Field key={key} label={label}>
                          <Textarea
                            rows={height}
                            required={key === 'description' || key === 'detail'}
                            aria-label={label}
                            value={editing.entry[key] ?? ''}
                            onChange={e => setEditing({...editing, entry:{...editing.entry, [key]:e.target.value}})}
                          />
                        </Field>
                      ))}
                      <Field label="来源引用" hint="每行一条；可写文件路径、URL 或证据 ID。">
                        <Textarea
                          rows={3}
                          aria-label="来源引用"
                          className="font-mono text-[12px]"
                          value={(editing.entry.source_refs ?? []).join('\n')}
                          onChange={e => setEditing({...editing, entry: {...editing.entry, source_refs:e.target.value.split('\n')}})}
                        />
                      </Field>
                      <Field label="标签" hint="逗号分隔；会出现在左侧标签筛选里。">
                        <Input aria-label="标签" value={(editing.entry.tags ?? []).join(',')} onChange={e => setEditing({...editing, entry: {...editing.entry, tags:e.target.value.split(',')}})} />
                      </Field>
                      <Field label="为什么可以跨项目复用" hint="晋升理由随候选一起保存，发布前请写清楚适用边界。">
                        <Textarea required rows={3} aria-label="为什么可以跨项目复用" value={editing.reason} onChange={e => setEditing({...editing, reason:e.target.value})} />
                      </Field>
                      {editing.entry.commands?.length ? (
                        <Callout tone="info">原条目包含验证命令，保存时会一并保留。发布前请核对其适用条件。</Callout>
                      ) : null}
                      <div className="flex flex-wrap items-center gap-2 border-t border-hairline pt-4">
                        <Button disabled={busy} type="submit" size="sm" aria-busy={busy}>
                          {busy && <Spinner size="sm" className="h-3.5 w-3.5" />}
                          {busy ? '保存中…' : '保存候选'}
                        </Button>
                        <Button type="button" variant="outline" size="sm" disabled={busy} onClick={() => setEditing(null)}>取消</Button>
                      </div>
                    </fieldset>
                  </form>
                </Panel>
              ) : current ? (
                <Panel
                  title={<span className="truncate" title={current.entry.title}>{current.entry.title}</span>}
                  extra={
                    <div className="flex flex-wrap items-center justify-end gap-1.5">
                      <Badge variant="muted" className="hidden font-mono sm:inline-flex">{current.entry.id}</Badge>
                      <Button size="xs" variant="outline" disabled={busy} onClick={() => edit(current.entry, current.candidate)}>编辑候选</Button>
                      <Button size="xs" variant="ghost" disabled={busy} className="text-ink-2 hover:text-destructive" onClick={remove}>
                        <Trash2 className="h-3.5 w-3.5" />{current.candidate ? '删除候选' : '删除知识'}
                      </Button>
                      {current.candidate && !current.candidate.published && (
                        <Button size="xs" variant="soft" disabled={busy} aria-busy={busy} onClick={() => act(() => api.publishKB(current.candidate!.id, current.candidate!.version), '已发布，所有项目的世界知识查询立即生效。')}>
                          <Upload className="h-3.5 w-3.5" />发布到世界 KB
                        </Button>
                      )}
                      {(!current.candidate || current.candidate.published) && (
                        <Button size="xs" variant="outline" asChild>
                          <a href={`/api/kb/export?id=${encodeURIComponent(current.entry.id)}`} download>
                            <Download className="h-3.5 w-3.5" />导出此条
                          </a>
                        </Button>
                      )}
                    </div>
                  }
                  bodyClassName="space-y-4"
                >
                  <div className="flex min-w-0 flex-wrap items-center gap-2">
                    <Badge variant="muted" className="font-mono sm:hidden">{current.entry.id}</Badge>
                    {current.entry.tags?.map(t => (
                      <Chip key={t} active={tag === t} onClick={() => setTag(tag === t ? null : t)} className="h-6 px-2 text-[11px]">{t}</Chip>
                    ))}
                  </div>
                  <p className="text-[13px] leading-6 text-ink-2">{current.entry.description}</p>
                  {current.entry.applicability && (
                    <div className="rounded-lg border border-primary/20 bg-primary/[0.05] p-4">
                      <h3 className="tw-eyebrow mb-2 normal-case tracking-normal">适用范围与前提条件</h3>
                      <p className="whitespace-pre-wrap break-words text-[13px] leading-6 text-ink-2">{current.entry.applicability}</p>
                    </div>
                  )}
                  <p className="whitespace-pre-wrap break-words text-[13px] leading-7 text-ink-2">{current.entry.detail}</p>
                  {current.entry.commands?.map((cmd, i) => (
                    <pre key={i} className="tw-break overflow-auto rounded-lg border border-hairline bg-surface-2/50 p-3 font-mono text-[12px] text-ink-2">{cmd}</pre>
                  ))}
                  {current.candidate && (
                    <div className="rounded-lg border border-hairline bg-surface-2/40 p-4 text-[12px] leading-6">
                      <p className="font-medium text-ink">晋升理由</p>
                      <p className="mt-1 whitespace-pre-wrap break-words text-ink-2">{current.candidate.reason}</p>
                      <p className="mt-2 break-all font-mono text-[11px] text-ink-3">
                        {current.candidate.origin} · 候选 v{current.candidate.version}
                      </p>
                    </div>
                  )}
                  <footer className="space-y-2 border-t border-hairline pt-4 text-[12px] leading-6 text-ink-3">
                    <p className="whitespace-pre-wrap break-words">{current.entry.source || '来源见引用'}</p>
                    {current.entry.source_refs?.map((ref, i) => <p key={i} className="break-all font-mono">{ref}</p>)}
                  </footer>
                </Panel>
              ) : (
                <div className="tw-panel rounded-xl">
                  <EmptyState
                    icon={BookOpen}
                    title="尚无知识"
                    description="从左侧选择条目，或点右上角「新建候选」补充事实与来源后发布；导入内置包后也会出现在这里。"
                    action={<Button size="sm" variant="outline" disabled={busy} onClick={() => edit(blank())}><FilePlus2 className="h-3.5 w-3.5" />新建候选</Button>}
                  />
                </div>
              )}

              <details className="rounded-lg border border-hairline bg-surface-2/40 p-3 text-[12px] leading-6">
                <summary className="cursor-pointer font-medium text-ink">如何把世界知识变成仓库内置？</summary>
                <p className="mt-2 text-ink-2">导出内置包后，在仓库运行下面的导入脚本。脚本只写内容 JSON，不操作 Git；随后由你检查、测试并提交。重新构建 tw 后进入 exe。</p>
                <pre className="mt-2 overflow-auto rounded bg-surface-2 p-2 font-mono text-[11px] leading-5 text-ink-2">{'pwsh -File .\\scripts\\import-kb.ps1 -Archive C:\\路径\\tianwu-kb.zip\ngit diff -- internal/kb/assets/entries\ngo test ./internal/kb\ngit add internal/kb/assets/entries\ngit commit -m "更新内置世界知识"\ngit push'}</pre>
                <p className="mt-2 text-ink-2">页面删除只影响当前实例；全库导出包会列出删除 ID。需要同步删除仓库里的对应 JSON 时，导入命令加 <code className="font-mono text-[11px] text-ink">-ApplyDeletions</code>。这不会删除包中未列出的其他知识。</p>
              </details>
            </div>
          </section>
        </div>
      </div>
    </Page>
  )
}
