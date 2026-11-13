import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { BookOpen, Download, FilePlus2, RefreshCw, Search, Upload, Trash2 } from 'lucide-react'
import { Badge, Button, Callout, Chip, EmptyState, Input, SkeletonList, useConfirm } from '@cyber/ui'
import { Page } from '@/components/Page'
import { api } from '@/lib/api'
import { useDebounced, useLoad } from '@/lib/hooks'
import type { KBCandidate, KBEntry, Playbook } from '@/lib/types'

const blank = (): KBEntry => ({id: '', title: '', description: '', detail: '', source: '', tags: [], source_refs: []})
const lines = (s: string) => s.split('\n').map(v => v.trim()).filter(Boolean)
const field = 'w-full rounded-md border border-border bg-background px-3 py-2 text-[13px] outline-none focus:border-primary'

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
  const refresh = () => { list.reload(); drafts.reload() }
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
  return <Page title="世界知识" subtitle={`${entries.length} 条已发布 · ${candidates.filter(c => !c.published).length} 条待晋升 · 实例共享，按需检索`} actions={<>
    <Button size="xs" variant="outline" disabled={busy} onClick={() => act(api.reloadKB, '已重新加载磁盘条目，查询立即使用新版本。')}><RefreshCw className="h-3.5 w-3.5" />重新加载</Button>
    <a href="/api/kb/export" className="inline-flex items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-xs"><Download className="h-3.5 w-3.5" />导出内置包</a>
    <Button size="xs" disabled={busy} onClick={() => edit(blank())}><FilePlus2 className="h-3.5 w-3.5" />新建候选</Button>
  </>} scroll={false}>
    <div className="flex h-full min-h-0 flex-col overflow-y-auto md:flex-row md:overflow-hidden">
      <aside className="flex h-[min(42vh,22rem)] min-h-[280px] w-full shrink-0 flex-col border-b border-border md:h-auto md:min-h-0 md:w-[320px] md:border-b-0 md:border-r xl:w-[360px]">
        <div className="shrink-0 space-y-3 border-b border-border p-3">
          <div className="flex gap-1 rounded-lg bg-muted/50 p-1">{(['published', 'candidates'] as const).map(t => <button key={t} disabled={busy} onClick={() => {setTab(t); setSelected(''); setEditing(null)}} className={`flex-1 rounded-md py-1.5 text-xs ${tab === t ? 'bg-background font-medium shadow-sm' : 'text-muted-foreground'}`}>{t === 'published' ? '已发布知识' : '晋升候选'}</button>)}</div>
          <div className="relative"><Search className="absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" /><Input aria-label="搜索知识" value={q} onChange={e => setQ(e.target.value)} placeholder="搜索知识、适用条件、来源" className="h-9 pl-8 text-xs" /></div>
          <div className="flex max-h-16 flex-wrap gap-1 overflow-auto">{tags.map(t => <Chip key={t} active={tag === t} onClick={() => setTag(tag === t ? null : t)} className="text-[11px]">{t}</Chip>)}</div>
        </div>
        <div className="min-h-0 flex-1 overflow-auto p-2">
          {(list.loading || drafts.loading) && <SkeletonList count={5} />}
          {(list.error || drafts.error) && <Callout tone="destructive">{list.error || drafts.error}</Callout>}
          {filtered.map(row => <button key={row.key} disabled={busy} onClick={() => {setSelected(row.key); setEditing(null)}} className={`mb-1 w-full rounded-lg px-3 py-3 text-left transition-colors ${current?.key === row.key ? 'bg-primary/10' : 'hover:bg-muted/60'}`}>
            <div className="flex items-center gap-2"><span className="truncate text-[13px] font-medium">{row.entry.title}</span>{row.candidate && <Badge variant={row.candidate.published ? 'success' : 'warning'} size="sm">{row.candidate.published ? '已发布' : '候选'}</Badge>}</div>
            <p className="mt-1 line-clamp-2 text-xs leading-5 text-muted-foreground">{row.entry.description}</p><p className="mt-1.5 font-mono text-[10px] text-muted-foreground">{row.entry.id}</p>
          </button>)}
          {!list.loading && !drafts.loading && filtered.length === 0 && <p className="p-4 text-xs text-muted-foreground">暂无匹配条目。</p>}
        </div>
        <div className="shrink-0 border-t border-border p-3 text-[11px] text-muted-foreground">显示 {filtered.length} / {rows.length} · <button disabled={busy} onClick={refresh} className="text-primary">刷新列表</button></div>
      </aside>
      <section className="min-h-[280px] min-w-0 flex-1 overflow-auto md:min-h-0"><div className="mx-auto max-w-3xl space-y-4 p-4 sm:p-6">
        {error && <Callout tone="destructive">{error}</Callout>}{notice && <Callout>{notice}</Callout>}
        {source.error && <Callout tone="destructive">来源战场知识读取失败：{source.error}</Callout>}
        {source.data && <details className="rounded-lg border border-border p-3 text-xs leading-6"><summary className="cursor-pointer font-medium">来源战场知识：{source.data.title}（仅供编辑参考）</summary><p className="mt-2">{source.data.when}</p><pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words">{source.data.doc}</pre><p className="mt-2 text-muted-foreground">请人工提取通用事实、补齐来源和适用条件。原文不会自动进入世界 KB。</p></details>}
        <details className="rounded-lg border border-border p-3 text-xs leading-6"><summary className="cursor-pointer font-medium">如何把世界知识变成仓库内置？</summary><p className="mt-2">导出内置包后，在仓库运行下面的导入脚本。脚本只写内容 JSON，不操作 Git；随后由你检查、测试并提交。重新构建 tw 后进入 exe。</p><pre className="mt-2 overflow-auto rounded bg-muted/30 p-2">{'pwsh -File .\\scripts\\import-kb.ps1 -Archive C:\\路径\\tianwu-kb.zip\ngit diff -- internal/kb/assets/entries\ngo test ./internal/kb\ngit add internal/kb/assets/entries\ngit commit -m "更新内置世界知识"\ngit push'}</pre><p className="mt-2">页面删除只影响当前实例；全库导出包会列出删除 ID。需要同步删除仓库里的对应 JSON 时，导入命令加 <code>-ApplyDeletions</code>。这不会删除包中未列出的其他知识。</p></details>
        {editing ? <form onSubmit={save}><fieldset disabled={busy} className="space-y-4">
          <div><h2 className="text-lg font-semibold">{editing.id ? '编辑晋升候选' : '提名世界知识'}</h2><p className="mt-1 text-xs leading-5 text-muted-foreground">保留通用事实、适用条件与来源。项目凭据、临时环境状态和工具教程留在原处。</p></div>
          <div className="grid gap-3 sm:grid-cols-2"><label className="space-y-1 text-xs">稳定 ID<Input required disabled={editing.id_locked} value={editing.entry.id} onChange={e => setEditing({...editing, base_revision:list.data?.deleted_revisions?.[e.target.value] ?? '', entry: {...editing.entry, id:e.target.value}})} placeholder="例如 protocol-example" /></label><label className="space-y-1 text-xs">标题<Input required value={editing.entry.title} onChange={e => setEditing({...editing, entry: {...editing.entry, title:e.target.value}})} /></label></div>
          {!editing.id_locked && editing.base_revision && <Callout>此 ID 在当前实例已删除；显式发布后将重新收录。</Callout>}
          {([['description', '摘要', 2], ['detail', '事实正文', 7], ['applicability', '适用范围与前提条件', 3], ['source', '来源说明', 2]] as const).map(([key, label, height]) => <label key={key} className="block space-y-1 text-xs">{label}<textarea className={field} rows={height} required={key === 'description' || key === 'detail'} value={editing.entry[key] ?? ''} onChange={e => setEditing({...editing, entry:{...editing.entry, [key]:e.target.value}})} /></label>)}
          <label className="block space-y-1 text-xs">来源引用（每行一条）<textarea className={field} rows={3} value={(editing.entry.source_refs ?? []).join('\n')} onChange={e => setEditing({...editing, entry:{...editing.entry, source_refs:e.target.value.split('\n')}})} /></label>
          <label className="block space-y-1 text-xs">标签（逗号分隔）<Input value={(editing.entry.tags ?? []).join(',')} onChange={e => setEditing({...editing, entry:{...editing.entry, tags:e.target.value.split(',')}})} /></label>
          <label className="block space-y-1 text-xs">为什么可以跨项目复用<textarea className={field} required rows={3} value={editing.reason} onChange={e => setEditing({...editing, reason:e.target.value})} /></label>
          {editing.entry.commands?.length ? <Callout>原条目包含验证命令，保存时会一并保留。发布前请核对其适用条件。</Callout> : null}
          <div className="flex gap-2"><Button disabled={busy} type="submit">保存候选</Button><Button type="button" variant="ghost" disabled={busy} onClick={() => setEditing(null)}>取消</Button></div>
        </fieldset></form> : current ? <article>
          <div className="flex flex-wrap items-center gap-2"><h2 className="text-lg font-semibold">{current.entry.title}</h2><Badge variant="muted">{current.entry.id}</Badge></div><p className="mt-2 text-[13px] leading-6 text-muted-foreground">{current.entry.description}</p>
          <div className="mt-4 flex flex-wrap gap-2"><Button size="xs" variant="outline" disabled={busy} onClick={() => edit(current.entry, current.candidate)}>编辑候选</Button>
            <Button size="xs" variant="ghost" disabled={busy} onClick={remove}><Trash2 className="h-3 w-3" />{current.candidate ? '删除候选' : '删除知识'}</Button>
            {current.candidate && !current.candidate.published && <Button size="xs" disabled={busy} onClick={() => act(() => api.publishKB(current.candidate!.id, current.candidate!.version), '已发布，所有项目的世界知识查询立即生效。')}><Upload className="h-3 w-3" />发布到世界 KB</Button>}
            {(!current.candidate || current.candidate.published) && <a className="inline-flex items-center gap-1.5 px-2 text-xs text-primary" href={`/api/kb/export?id=${encodeURIComponent(current.entry.id)}`}><Download className="h-3 w-3" />导出此条</a>}
          </div>
          {current.entry.applicability && <div className="mt-5 rounded-lg border border-primary/20 bg-primary/5 p-4"><h3 className="mb-2 text-xs font-medium">适用范围与前提条件</h3><p className="whitespace-pre-wrap break-words text-[13px] leading-6">{current.entry.applicability}</p></div>}
          <p className="mt-5 whitespace-pre-wrap break-words text-[13px] leading-7">{current.entry.detail}</p>{current.entry.commands?.map((cmd, i) => <pre key={i} className="mt-3 overflow-auto rounded-lg border border-border bg-muted/30 p-3 text-xs">{cmd}</pre>)}
          {current.candidate && <div className="mt-5 rounded-lg border border-border p-4 text-xs leading-6"><p className="font-medium">晋升理由</p><p className="whitespace-pre-wrap break-words">{current.candidate.reason}</p><p className="mt-2 break-all text-muted-foreground">{current.candidate.origin} · 候选 v{current.candidate.version}</p></div>}
          <footer className="mt-6 space-y-2 border-t border-border pt-4 text-xs leading-6 text-muted-foreground"><p className="whitespace-pre-wrap break-words">{current.entry.source || '来源见引用'}</p>{current.entry.source_refs?.map((ref, i) => <p key={i} className="break-all">{ref}</p>)}</footer>
        </article> : <EmptyState icon={BookOpen} title="尚无知识" description="新建候选，补充事实与来源后发布。" />}
      </div></section>
    </div>
  </Page>
}
