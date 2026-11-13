import { useMemo, useState } from 'react'
import { Download, Eye } from 'lucide-react'
import { Button, Callout, EmptyState, SkeletonList } from '@cyber/ui'
import { EvidencePreview } from '@/components/EvidencePreview'
import { Page } from '@/components/Page'
import { api } from '@/lib/api'
import { evidencePreviewFile, type PreviewFile } from '@/lib/evidence-preview'
import { fmtBytes, fmtTime, shortID } from '@/lib/format'
import { useLoad } from '@/lib/hooks'

/**
 * The evidence list is a reading surface, not a download tray: a click previews
 * the original in the sheet, and the row's download button is the only action
 * that saves a file. See EvidencePreview for why HTML is shown as source.
 *
 * Long engagements collect thousands of files; mounting every row at once made
 * opening this panel a ~0.8 s freeze (3.6 k rows of table layout on the main
 * thread). It renders a window of rows and grows it on demand, so the panel
 * opens immediately and the rest is one click away.
 */
const PAGE_ROWS = 200

export function EvidenceView() {
  const list = useLoad(api.evidence, [])
  const [preview, setPreview] = useState<PreviewFile | null>(null)
  const [shown, setShown] = useState(PAGE_ROWS)
  const items = useMemo(
    () => [...(list.data ?? [])].sort((a, b) => b.created_at.localeCompare(a.created_at)),
    [list.data],
  )
  const total = useMemo(() => items.reduce((n, e) => n + e.size, 0), [items])
  const visible = items.slice(0, shown)

  return (
    <Page
      title="证据"
      subtitle={`${items.length} 个文件 · ${fmtBytes(total)} · 点击预览；需要原件时再下载`}
    >
      {list.error && <Callout tone="destructive" className="mb-2">{list.error}</Callout>}
      {list.loading && <SkeletonList count={6} />}
      {!list.loading && items.length === 0 && (
        <EmptyState title="还没有证据" description="执行者产出新文件后，事实意图图按 mtime 收集；.tw/ 是交接通道，不进证据。" />
      )}

      {items.length > 0 && (
        <>
        <div className="overflow-hidden rounded-md border border-border">
          <table className="w-full table-fixed text-left text-[12px]">
            <thead className="bg-muted/50 text-[11px] uppercase tracking-wider text-muted-foreground">
              <tr>
                <th className="w-[42%] px-2 py-1.5 font-medium">文件</th>
                <th className="w-[9%] px-2 py-1.5 font-medium">大小</th>
                <th className="w-[12%] px-2 py-1.5 font-medium">类型</th>
                <th className="w-[13%] px-2 py-1.5 font-medium">意图</th>
                <th className="w-[14%] px-2 py-1.5 font-medium">时间</th>
                <th className="w-[10%] px-2 py-1.5 text-right font-medium">
                  <span className="sr-only">操作</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {visible.map((e) => (
                <tr
                  key={e.id}
                  onClick={() => setPreview(evidencePreviewFile(e))}
                  title="点击预览"
                  className="cursor-pointer border-t border-border/60 hover:bg-accent/30"
                >
                  <td className="px-2 py-1.5">
                    <button
                      type="button"
                      onClick={(ev) => {
                        ev.stopPropagation()
                        setPreview(evidencePreviewFile(e))
                      }}
                      className="tw-break flex w-full items-center gap-1.5 text-left font-mono text-foreground/90 hover:text-primary"
                    >
                      <Eye className="h-3 w-3 shrink-0 opacity-60" />
                      {e.uri}
                    </button>
                  </td>
                  <td className="px-2 py-1.5 font-mono text-muted-foreground">{fmtBytes(e.size)}</td>
                  <td className="truncate px-2 py-1.5 text-muted-foreground">{e.mime || '—'}</td>
                  <td className="px-2 py-1.5 font-mono text-muted-foreground">{shortID(e.intent_id)}</td>
                  <td className="px-2 py-1.5 font-mono text-muted-foreground">{fmtTime(e.created_at)}</td>
                  <td className="px-2 py-1.5 text-right">
                    <Button asChild size="icon-xs" variant="ghost">
                      <a
                        href={api.evidenceDownloadURL(e.id)}
                        download={e.name || e.uri.split('/').pop() || undefined}
                        title="下载原文件"
                        aria-label={`下载 ${e.uri}`}
                        onClick={(ev) => ev.stopPropagation()}
                      >
                        <Download className="h-3 w-3" />
                      </a>
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {items.length > shown && (
          <div className="mt-2 flex items-center gap-2 text-[11px] text-muted-foreground">
            <Button size="xs" variant="outline" onClick={() => setShown((n) => n + PAGE_ROWS)}>
              再显示 {Math.min(PAGE_ROWS, items.length - shown)} 条
            </Button>
            <span>
              已显示 {shown} / {items.length}（按时间倒序）
            </span>
          </div>
        )}
        </>
      )}

      <EvidencePreview file={preview} onOpenChange={(open) => !open && setPreview(null)} />
    </Page>
  )
}
