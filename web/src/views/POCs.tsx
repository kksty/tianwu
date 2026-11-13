import { useMemo, useState } from 'react'
import { ChevronLeft, ChevronRight, FileCode2, Search, TriangleAlert, Upload } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Chip,
  EmptyState,
  Field,
  Input,
  SkeletonList,
  Spinner,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtBytes } from '@/lib/format'
import { useDebounced, useLoad } from '@/lib/hooks'

const severityVariant: Record<string, 'danger' | 'caution' | 'warning' | 'info' | 'muted'> = {
  critical: 'danger',
  high: 'caution',
  medium: 'warning',
  low: 'info',
  info: 'muted',
}

const severityDot: Record<string, string> = {
  critical: 'bg-destructive',
  high: 'bg-caution',
  medium: 'bg-warning',
  low: 'bg-info',
  info: 'bg-ink-3',
}

/**
 * One page of the library. A community import is thousands of templates, so the
 * list is filtered and paged by the server (see /api/pocs): shipping and
 * rendering every row at once froze the page. Rendering at most PAGE_SIZE rows
 * keeps the panel responsive while the search box still spans the whole library.
 */
const PAGE_SIZE = 100

export function POCs() {
  const [q, setQ] = useState('')
  const query = useDebounced(q, 200)
  const [offset, setOffset] = useState(0)
  const [path, setPath] = useState('')
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  const [noteError, setNoteError] = useState(false)
  const [severity, setSeverity] = useState('')

  const list = useLoad(() => api.pocs({ q: query, offset, limit: PAGE_SIZE }), [query, offset])
  const pocs = list.data?.pocs ?? []
  const total = list.data?.total ?? pocs.length
  const matched = list.data?.matched ?? pocs.length
  const searching = query.trim() !== ''
  const page = Math.floor(offset / PAGE_SIZE) + 1
  const pageCount = Math.max(1, Math.ceil(matched / PAGE_SIZE))
  const hasPrev = offset > 0
  const hasNext = offset + PAGE_SIZE < matched
  // Severity is not a server filter, so these chips narrow the page on screen and
  // say so; the search box is still what spans the whole library.
  const severities = useMemo(
    () => [...new Set(pocs.map(p => p.severity?.toLowerCase()).filter((s): s is string => Boolean(s)))].sort(),
    [pocs],
  )
  const visiblePocs = useMemo(
    () => (severity ? pocs.filter(p => p.severity?.toLowerCase() === severity) : pocs),
    [pocs, severity],
  )
  const filtered = Boolean(severity)

  function search(next: string) {
    setQ(next)
    setOffset(0)
  }

  async function runImport() {
    if (!path.trim()) {
      setNote('需要模板文件或目录的路径')
      setNoteError(true)
      return
    }
    setBusy(true)
    setNote('')
    setNoteError(false)
    try {
      const res = await api.importPocs(path.trim())
      setNote(`导入 ${res.imported ?? 0}，跳过 ${res.skipped ?? 0}${res.errors?.length ? `，错误 ${res.errors.join('; ')}` : ''}`)
      setPath('')
      setOffset(0)
      list.reload()
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e))
      setNoteError(true)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Page
      title="POC 库"
      subtitle={`${total} 个模板${searching ? ` · 匹配 ${matched}` : ''} · 实例级共享 · 执行者 neutron -t 直接指向这个目录`}
    >
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <div className="min-w-0 space-y-3 xl:col-span-2">
          {/* One h-8 toolbar: search, severity chips, match count. */}
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative min-w-[180px] flex-1">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" aria-hidden="true" />
              <Input
                value={q}
                onChange={(e) => search(e.target.value)}
                placeholder="搜索 id / 名称 / 标签 / 文件名"
                className="h-8 pl-8 text-[12px]"
                aria-label="搜索模板"
              />
            </div>
            {searching && (
              <Button size="xs" variant="ghost" onClick={() => search('')}>
                清空
              </Button>
            )}
            <span className="tw-nums shrink-0 font-mono text-[11px] text-ink-3">
              {searching ? `${matched} / ${total}` : `${total} 个`}
            </span>
            {severities.length > 0 && (
              <>
                <span className="mx-0.5 hidden h-4 w-px bg-hairline sm:block" />
                <span className="text-[11px] text-ink-3">严重级别</span>
                <Chip active={!severity} onClick={() => setSeverity('')} className="h-7 px-2 text-[11px]">全部</Chip>
                {severities.map(s => (
                  <Chip
                    key={s}
                    active={severity === s}
                    onClick={() => setSeverity(severity === s ? '' : s)}
                    title={`按本页模板的严重级别筛选（不影响搜索范围）`}
                    className="h-7 gap-1.5 px-2 text-[11px]"
                  >
                    <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${severityDot[s] ?? 'bg-ink-3'}`} />
                    {s}
                  </Chip>
                ))}
                {filtered && <span className="text-[11px] text-ink-3">本页筛选</span>}
              </>
            )}
          </div>

          {list.error && (
            <div role="alert">
              <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                  <span className="min-w-0 tw-break">{list.error}</span>
                  <Button size="xs" variant="outline" disabled={list.loading} onClick={list.reload}>重试</Button>
                </div>
              </Callout>
            </div>
          )}

          <Panel
            title="模板"
            extra={
              <span className="tw-nums font-mono text-[11px] text-ink-3">
                {filtered ? `本页 ${visiblePocs.length} / ${pocs.length}` : `${matched} 个匹配`}
              </span>
            }
            bodyClassName="p-0"
          >
            {list.loading && pocs.length === 0 && (
              <div className="p-4">
                <SkeletonList count={5} />
              </div>
            )}
            {!list.loading && pocs.length === 0 && (
              searching ? (
                <EmptyState
                  icon={Search}
                  title="没有匹配的模板"
                  description="换个关键词；匹配 id、名称、标签和文件名。"
                  action={<Button size="sm" variant="outline" onClick={() => search('')}>清空搜索</Button>}
                />
              ) : (
                <EmptyState
                  icon={FileCode2}
                  title="库是空的"
                  description="导入 nuclei / neutron 的 YAML：在右侧填写文件或目录后点「导入」。不需要了直接删文件，没有获取门槛。"
                  action={
                    <Button size="sm" variant="outline" onClick={() => document.getElementById('poc-import-path')?.focus()}>
                      去填写导入路径
                    </Button>
                  }
                />
              )
            )}
            {!list.loading && pocs.length > 0 && visiblePocs.length === 0 && (
              <EmptyState
                compact
                icon={Search}
                title="本页没有该严重级别的模板"
                description="严重级别筛选只作用于当前页；翻页或清空筛选可查看其他模板。"
                action={<Button size="sm" variant="outline" onClick={() => setSeverity('')}>清空筛选</Button>}
              />
            )}
            {visiblePocs.length > 0 && (
              <div className="divide-y divide-hairline">
                {visiblePocs.map((p) => (
                  <div
                    key={p.id}
                    data-testid="poc-row"
                    data-poc-id={p.id}
                    className="flex min-h-[48px] items-center gap-2.5 px-3 py-2.5 transition-colors hover:bg-surface-2/60"
                  >
                    <FileCode2 className="h-4 w-4 shrink-0 text-ink-3" aria-hidden="true" />
                    <div className="min-w-0 flex-1">
                      <div className="flex min-w-0 items-center gap-2">
                        <span className="truncate text-[13px] font-medium leading-5 text-ink">{p.name || p.id}</span>
                        {p.severity && (
                          <Badge size="sm" variant={severityVariant[p.severity.toLowerCase()] ?? 'muted'} className="shrink-0">
                            {p.severity}
                          </Badge>
                        )}
                      </div>
                      <div className="mt-0.5 truncate font-mono text-[11px] leading-4 text-ink-3">
                        {p.id} · {p.file}
                        {p.tags ? ` · ${p.tags}` : ''}
                      </div>
                    </div>
                    <span className="tw-nums shrink-0 font-mono text-[11px] text-ink-3">
                      {fmtBytes(p.size)}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </Panel>

          {matched > PAGE_SIZE && (
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="tw-nums font-mono text-[11px] text-ink-3">
                第 {page} / {pageCount} 页
              </span>
              <div className="flex items-center gap-1.5">
                <Button
                  size="xs"
                  variant="outline"
                  disabled={!hasPrev}
                  onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                >
                  <ChevronLeft className="h-3.5 w-3.5" /> 上一页
                </Button>
                <Button
                  size="xs"
                  variant="outline"
                  disabled={!hasNext}
                  onClick={() => setOffset(offset + PAGE_SIZE)}
                >
                  下一页 <ChevronRight className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          )}
        </div>

        <Panel title="导入模板" extra={<Badge variant="outline">实例级</Badge>} bodyClassName="space-y-3">
          <Field label="文件或目录" hint="相对 <data> 目录，或绝对路径；目录会递归导入">
            <Input
              id="poc-import-path"
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="pocs/nuclei/cves"
              className="font-mono text-[12px]"
              disabled={busy}
              aria-label="模板文件或目录"
            />
          </Field>
          <Button size="sm" onClick={runImport} disabled={busy} aria-busy={busy}>
            {busy ? <Spinner size="sm" className="h-3.5 w-3.5" /> : <Upload className="h-3.5 w-3.5" />}
            {busy ? '导入中…' : '导入'}
          </Button>
          {list.data?.dir && (
            <div className="tw-inset tw-break rounded-lg px-2.5 py-2 font-mono text-[11px] text-ink-3">{list.data.dir}</div>
          )}
          {note && (
            <div role={noteError ? 'alert' : 'status'}>
              <Callout tone={noteError ? 'destructive' : 'success'}>{note}</Callout>
            </div>
          )}
          <p className="text-[11px] leading-4 text-ink-3">
            导入只复制模板文件，不执行模板；同名 ID 会被覆盖。删除模板直接删文件即可。
          </p>
        </Panel>
      </div>
    </Page>
  )
}
