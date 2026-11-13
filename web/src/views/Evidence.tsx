import { useMemo, useState } from 'react'
import { Download, Eye, RefreshCw } from 'lucide-react'
import { Button, Callout, EmptyState, SkeletonList } from '@cyber/ui'
import { cn } from '@cyber/theme'
import { EvidencePreview } from '@/components/EvidencePreview'
import { Page, Panel } from '@/components/Page'
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
      actions={
        <Button size="sm" variant="outline" onClick={list.reload} disabled={list.loading || list.refreshing}>
          <RefreshCw className={cn('h-3.5 w-3.5', list.refreshing && 'animate-spin')} /> 刷新
        </Button>
      }
    >
      {list.error && (
        <Callout tone="destructive" className="mb-3">
          {list.error}
          <Button size="xs" variant="outline" className="ml-2" onClick={list.reload}>
            重试
          </Button>
        </Callout>
      )}

      <Panel
        title="证据文件"
        extra={
          <span className="tw-nums font-mono text-[11px] text-ink-3">
            {items.length} 个 · 合计 {fmtBytes(total)}
          </span>
        }
        bodyClassName="p-0"
      >
        {list.loading && (
          <div className="p-4">
            <SkeletonList count={6} />
          </div>
        )}

        {!list.loading && items.length === 0 && (
          <EmptyState
            icon={Eye}
            title="还没有证据"
            description="执行者产出新文件后，事实意图图按 mtime 收集；.tw/ 是交接通道，不进证据。"
          />
        )}

        {visible.length > 0 && (
          <div className="divide-y divide-hairline">
            {visible.map((e) => {
              const active = preview?.key === e.id
              return (
                <div
                  key={e.id}
                  className={cn(
                    'group flex items-start gap-2 px-2.5 py-2.5 transition-colors',
                    active
                      ? 'bg-primary/[0.09] shadow-[inset_2px_0_0_var(--primary)]'
                      : 'hover:bg-surface-2 focus-within:bg-surface-2',
                  )}
                >
                  <button
                    type="button"
                    onClick={() => setPreview(evidencePreviewFile(e))}
                    title="点击预览"
                    className="flex min-w-0 flex-1 items-start gap-2 rounded-md text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                  >
                    <Eye className="mt-0.5 h-3.5 w-3.5 shrink-0 text-ink-3" />
                    <span className="min-w-0 flex-1">
                      <span className="tw-break block font-mono text-[13px] leading-5 text-ink">
                        {e.name || e.uri.split('/').pop() || e.uri}
                      </span>
                      <span className="tw-break mt-0.5 block font-mono text-[11px] leading-4 text-ink-3">
                        {e.uri}
                      </span>
                      <span className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[11px] leading-4 text-ink-3">
                        <span className="tw-break">{e.mime || '未知类型'}</span>
                        <span className="tw-nums font-mono">{fmtBytes(e.size)}</span>
                        <span className="font-mono">意图 {shortID(e.intent_id)}</span>
                      </span>
                    </span>
                  </button>
                  <div className="flex shrink-0 flex-col items-end gap-1">
                    <span className="tw-nums whitespace-nowrap font-mono text-[11px] leading-4 text-ink-3">
                      {fmtTime(e.created_at)}
                    </span>
                    <Button asChild size="icon-xs" variant="ghost">
                      <a
                        href={api.evidenceDownloadURL(e.id)}
                        download={e.name || e.uri.split('/').pop() || undefined}
                        title="下载原文件"
                        aria-label={`下载 ${e.uri}`}
                      >
                        <Download className="h-3.5 w-3.5" />
                      </a>
                    </Button>
                  </div>
                </div>
              )
            })}
          </div>
        )}

        {items.length > shown && (
          <div className="flex flex-wrap items-center gap-2 border-t border-hairline px-3 py-2.5 text-[11px] text-ink-3">
            <Button size="xs" variant="outline" onClick={() => setShown((n) => n + PAGE_ROWS)}>
              再显示 {Math.min(PAGE_ROWS, items.length - shown)} 条
            </Button>
            <span className="tw-nums font-mono">
              已显示 {shown} / {items.length}（按时间倒序）
            </span>
          </div>
        )}
      </Panel>

      <EvidencePreview file={preview} onOpenChange={(open) => !open && setPreview(null)} />
    </Page>
  )
}
