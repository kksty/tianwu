import { useState } from 'react'
import { ChevronLeft, ChevronRight, FileCode2, Search, Upload } from 'lucide-react'
import { Badge, Button, Callout, EmptyState, Field, Input, SkeletonList } from '@cyber/ui'
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

  const list = useLoad(() => api.pocs({ q: query, offset, limit: PAGE_SIZE }), [query, offset])
  const pocs = list.data?.pocs ?? []
  const total = list.data?.total ?? pocs.length
  const matched = list.data?.matched ?? pocs.length
  const searching = query.trim() !== ''
  const page = Math.floor(offset / PAGE_SIZE) + 1
  const pageCount = Math.max(1, Math.ceil(matched / PAGE_SIZE))
  const hasPrev = offset > 0
  const hasNext = offset + PAGE_SIZE < matched

  function search(next: string) {
    setQ(next)
    setOffset(0)
  }

  async function runImport() {
    if (!path.trim()) {
      setNote('需要模板文件或目录的路径')
      return
    }
    setBusy(true)
    setNote('')
    try {
      const res = await api.importPocs(path.trim())
      setNote(`导入 ${res.imported ?? 0}，跳过 ${res.skipped ?? 0}${res.errors?.length ? `，错误 ${res.errors.join('; ')}` : ''}`)
      setPath('')
      setOffset(0)
      list.reload()
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Page
      title="POC 库"
      subtitle={`${total} 个模板${searching ? ` · 匹配 ${matched}` : ''} · 实例级共享 · 执行者 neutron -t 直接指向这个目录`}
    >
      <div className="grid grid-cols-1 gap-3 xl:grid-cols-3">
        <div className="space-y-1.5 xl:col-span-2">
          <div className="flex items-center gap-2">
            <div className="relative min-w-0 flex-1">
              <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={q}
                onChange={(e) => search(e.target.value)}
                placeholder="搜索 id / 名称 / 标签 / 文件名"
                className="h-8 pl-7 text-[12px]"
                aria-label="搜索模板"
              />
            </div>
            {searching && (
              <Button size="xs" variant="ghost" onClick={() => search('')}>
                清空
              </Button>
            )}
            <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
              {searching ? `${matched} / ${total}` : `${total} 个`}
            </span>
          </div>

          {list.error && <Callout tone="destructive">{list.error}</Callout>}
          {list.loading && pocs.length === 0 && <SkeletonList count={5} />}
          {!list.loading && pocs.length === 0 && (
            searching ? (
              <EmptyState
                icon={Search}
                title="没有匹配的模板"
                description="换个关键词；匹配 id、名称、标签和文件名。"
              />
            ) : (
              <EmptyState
                icon={FileCode2}
                title="库是空的"
                description="导入 nuclei / neutron 的 YAML；不需要了直接删文件，没有获取门槛。"
              />
            )
          )}
          {pocs.map((p) => (
            <div
              key={p.id}
              data-testid="poc-row"
              data-poc-id={p.id}
              className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2"
            >
              <FileCode2 className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-ui text-foreground">{p.name || p.id}</span>
                  {p.severity && (
                    <Badge size="sm" variant={severityVariant[p.severity.toLowerCase()] ?? 'muted'}>
                      {p.severity}
                    </Badge>
                  )}
                </div>
                <div className="truncate font-mono text-[11px] text-muted-foreground">
                  {p.id} · {p.file}
                  {p.tags ? ` · ${p.tags}` : ''}
                </div>
              </div>
              <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
                {fmtBytes(p.size)}
              </span>
            </div>
          ))}

          {matched > PAGE_SIZE && (
            <div className="flex items-center justify-between gap-2 pt-1">
              <span className="font-mono text-[11px] text-muted-foreground">
                第 {page} / {pageCount} 页
              </span>
              <div className="flex items-center gap-1.5">
                <Button
                  size="xs"
                  variant="outline"
                  disabled={!hasPrev}
                  onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                >
                  <ChevronLeft className="h-3 w-3" /> 上一页
                </Button>
                <Button
                  size="xs"
                  variant="outline"
                  disabled={!hasNext}
                  onClick={() => setOffset(offset + PAGE_SIZE)}
                >
                  下一页 <ChevronRight className="h-3 w-3" />
                </Button>
              </div>
            </div>
          )}
        </div>

        <Panel title="导入模板">
          <div className="space-y-2">
            <Field label="文件或目录" hint="相对 <data> 目录，或绝对路径；目录会递归导入">
              <Input
                value={path}
                onChange={(e) => setPath(e.target.value)}
                placeholder="pocs/nuclei/cves"
                className="font-mono text-[12px]"
              />
            </Field>
            <Button size="sm" onClick={runImport} disabled={busy}>
              <Upload className="h-3 w-3" /> {busy ? '导入中…' : '导入'}
            </Button>
            {list.data?.dir && (
              <div className="tw-break font-mono text-[11px] text-muted-foreground">{list.data.dir}</div>
            )}
            {note && <div className="text-[12px] text-muted-foreground">{note}</div>}
          </div>
        </Panel>
      </div>
    </Page>
  )
}
