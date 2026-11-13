import { useEffect, useMemo, useRef, useState } from 'react'
import { CheckCircle2, Download, FileText, Plus, XCircle } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  EmptyState,
  ScrollArea,
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SkeletonList,
  StatusIndicator,
} from '@cyber/ui'
import { MiniMarkdown } from '@/components/MiniMarkdown'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtTime, statusKind, statusLabel } from '@/lib/format'
import { useLoad, usePoll } from '@/lib/hooks'
import { useSessionState } from '@/lib/session-state'
import type { ReportMeta, ReportPrecheck } from '@/lib/types'

/**
 * Reports are a real executor run: a precheck first counts what would land.
 * Unverified conclusions are only allowed as candidates, so a report that writes
 * them confirmed fails validation — the operator gets one chance to review them
 * first, but the precheck is advisory and never blocks the run (D100).
 *
 * The page is one list: every row is a generated report (id, what it found, when,
 * validation checks), and the single primary action is "生成报告". The empty
 * state explains the generate flow instead of leaving a blank panel.
 */
export function Reports({ onGoVerify }: { onGoVerify?: () => void } = {}) {
  const list = usePoll(api.reports, 4000)
  const progress = usePoll(api.reportPrecheck, 4000)
  const runs = usePoll(api.runs, 4000)
  const [pendingReport, setPendingReport] = useSessionState('report.pending.v1', '')
  const [failure, setFailure] = useState('')
  const pending = useMemo(() => {
    try {
      const value = JSON.parse(pendingReport)
      return Array.isArray(value.reports) && Array.isArray(value.runs) ? value as { reports: string[]; runs: string[] } : null
    } catch { return null }
  }, [pendingReport])
  const reportRuns = (runs.data ?? []).filter(r => r.purpose === 'report')
  const trackedRun = pending ? reportRuns.find(r => !pending.runs.includes(r.id)) : undefined
  const completed = pending ? (list.data ?? []).find(r => !pending.reports.includes(r.id) && r.status !== 'running') : undefined
  const generating = progress.data?.running || (list.data ?? []).some(r => r.status === 'running') || reportRuns.some(r => r.status === 'running')
  useEffect(() => {
    if (!pendingReport) return
    if (!pending) { setPendingReport(''); return }
    if (completed || (trackedRun && trackedRun.status !== 'running')) {
      setPendingReport('')
      list.reload()
      if (completed?.status === 'failed' || trackedRun?.status === 'failed' || trackedRun?.status === 'canceled') {
        setFailure('报告生成失败，请查看运行记录中的失败原因，修复后重新生成。')
      } else {
        setNote(completed ? '报告已生成，可在下方查看和下载。' : '报告运行已结束，正在更新报告列表。')
      }
    }
  }, [pendingReport, completed, trackedRun])
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  const [open, setOpen] = useState<ReportMeta | null>(null)
  // Non-zero means the precheck found unverified conclusions and the confirm
  // dialog is open.
  const [unverified, setUnverified] = useState(0)
  const queueLock = useRef(false)

  /** Queue the actual report run (the "生成报告" path, and "仍然生成"). */
  async function queueReport() {
    if (queueLock.current) return
    queueLock.current = true
    setBusy(true)
    setNote('')
    setFailure('')
    try {
      const [existingReports, existingRuns] = await Promise.all([api.reports(), api.runs()])
      await api.createReport()
      setNote('已排入生成队列：一次独立的报告撰写运行（不占用指挥官/执行者），写完才出现在列表里。')
      setPendingReport(JSON.stringify({ reports: existingReports.map(r => r.id), runs: existingRuns.map(r => r.id) }))
      list.reload()
      progress.reload()
      runs.reload()
    } catch (e) {
      setFailure(`提交失败：${e instanceof Error ? e.message : String(e)}。请检查连接后重试。`)
    } finally {
      queueLock.current = false
      setBusy(false)
    }
  }

  async function generate() {
    if (busy || generating || pendingReport) return
    setBusy(true)
    setNote('')
    try {
      let precheck: ReportPrecheck | null = null
      try {
        precheck = await api.reportPrecheck()
      } catch {
        // No precheck endpoint / request failed: keep the old behaviour and
        // generate directly instead of blocking on a probe.
        precheck = null
      }
      if (precheck?.running) { setNote('报告正在生成，将自动更新列表。'); progress.reload(); return }
      if (precheck && precheck.unverified > 0) {
        setUnverified(precheck.unverified)
        return
      }
      await queueReport()
    } finally {
      queueLock.current = false
      setBusy(false)
    }
  }

  const rows = list.data ?? []

  return (
    <Page
      title="报告"
      subtitle="汇总项目发现与证据，生成可下载报告；账本变更后会标记为过期"
      actions={
        <Button size="sm" onClick={generate} disabled={busy || !!generating || !!pendingReport}>
          <Plus className="h-3.5 w-3.5" /> {busy ? '提交中…' : generating ? '生成中…' : pendingReport ? '已排队' : '生成报告'}
        </Button>
      }
    >
      {(generating || pendingReport) && (
        <Callout className="mb-3">
          <span role="status">{generating ? '报告生成中，完成后会自动出现在下方。' : '已排队，正在等待报告运行。'}</span>
          <a className="ml-2 underline underline-offset-2" href="?view=runs">查看运行进度</a>
        </Callout>
      )}
      {pending && !generating && progress.data && !progress.error && !runs.error && !list.error && (
        <p className="mb-3 text-[12px] leading-relaxed text-ink-2">
          若等待没有进展，可先查看运行记录。
          <Button size="xs" variant="ghost" onClick={() => { setPendingReport(''); setNote('已结束等待；如需重新生成，请先核对运行记录。') }}>
            已核对，结束等待
          </Button>
        </p>
      )}
      {failure && <Callout tone="destructive" className="mb-3">{failure}</Callout>}
      {(list.error || progress.error || runs.error) && (
        <Callout tone="warning" className="mb-3">
          状态更新失败，当前显示上次读取的数据。
          <Button size="xs" variant="outline" disabled={list.refreshing || progress.refreshing || runs.refreshing} onClick={() => { list.reload(); progress.reload(); runs.reload() }}>
            重试更新
          </Button>
        </Callout>
      )}
      {note && <Callout tone="info" className="mb-3">{note}</Callout>}

      <Panel
        title="报告列表"
        extra={
          <span className="tw-nums font-mono text-[11px] text-ink-3">{rows.length} 份</span>
        }
        bodyClassName="p-0"
      >
        {list.loading && (
          <div className="p-4">
            <SkeletonList count={4} />
          </div>
        )}

        {!list.loading && !list.error && !generating && !pendingReport && rows.length === 0 && (
          <EmptyState
            icon={FileText}
            title="还没有报告"
            description="点右上角「生成报告」：先检查未复核结论，确认后排队一次独立的报告撰写运行（不依赖指挥官或执行者），写完才出现在这里；生成前可先完成复核。"
          />
        )}

        {rows.length > 0 && (
          <div className="divide-y divide-hairline">
            {rows.map((r) => (
              <div
                key={r.id}
                className={
                  'flex flex-wrap items-start gap-x-3 gap-y-2 px-2.5 py-2.5 transition-colors ' +
                  (open?.id === r.id
                    ? 'bg-primary/[0.09] shadow-[inset_2px_0_0_var(--primary)]'
                    : 'hover:bg-surface-2')
                }
              >
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <button
                      type="button"
                      onClick={() => setOpen(r)}
                      title="打开报告预览"
                      aria-expanded={open?.id === r.id}
                      className="tw-break rounded font-mono text-[13px] font-medium leading-5 text-ink hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                    >
                      {r.id}
                    </button>
                    <Badge size="sm" variant="secondary">
                      <span className="tw-nums font-mono">{r.findings}</span> 条结论
                    </Badge>
                    {r.status === 'stale' && (
                      <Badge size="sm" variant="warning">账本变更后过期</Badge>
                    )}
                  </div>
                  {r.checks && r.checks.length > 0 && (
                    <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1">
                      {r.checks.map((c, i) => (
                        <span
                          key={i}
                          title={c.detail}
                          className={`flex items-center gap-1 text-[11px] leading-4 ${c.ok ? 'text-success' : 'text-destructive'}`}
                        >
                          {c.ok ? <CheckCircle2 className="h-3 w-3" /> : <XCircle className="h-3 w-3" />}
                          {c.rule}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
                <div className="flex shrink-0 flex-col items-end gap-1.5">
                  <StatusIndicator compact status={statusKind(r.status)} label={statusLabel(r.status)} />
                  <span className="tw-nums whitespace-nowrap font-mono text-[11px] leading-4 text-ink-3">
                    {fmtTime(r.created_at)}
                    {r.finished_at ? ` → ${fmtTime(r.finished_at)}` : ''}
                  </span>
                  <a
                    className="flex items-center gap-1 text-[12px] text-ink-2 transition-colors hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                    href={api.reportFileURL(r.id, 'index.md')}
                    target="_blank"
                    rel="noreferrer"
                    title="下载报告正文 index.md"
                  >
                    <Download className="h-3 w-3" /> index.md
                  </a>
                </div>
              </div>
            ))}
          </div>
        )}
      </Panel>

      <ReportSheet report={open} onOpenChange={(o) => !o && setOpen(null)} />

      <Dialog open={unverified > 0} onOpenChange={(o) => !o && setUnverified(0)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>还有 {unverified} 条结论未复核</DialogTitle>
            <DialogDescription>
              未复核的结论在报告中只能作为 candidate，写成 confirmed 会被校验拒绝。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setUnverified(0)}>
              取消
            </Button>
            <Button
              variant="outline"
              disabled={!onGoVerify}
              title={onGoVerify ? '去漏洞页复核这些结论，再回来生成' : '当前页面没有复核入口'}
              onClick={() => {
                setUnverified(0)
                onGoVerify?.()
              }}
            >
              先去复核
            </Button>
            <Button
              disabled={busy}
              onClick={() => {
                setUnverified(0)
                void queueReport()
              }}
            >
              仍然生成
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Page>
  )
}

function ReportSheet({
  report,
  onOpenChange,
}: {
  report: ReportMeta | null
  onOpenChange: (v: boolean) => void
}) {
  const doc = useLoad(
    () => (report ? api.reportFileText(report.id, 'index.md') : Promise.resolve('')),
    [report?.id],
  )

  return (
    <Sheet open={!!report} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 sm:max-w-2xl">
        <SheetHeader className="shrink-0 space-y-2 border-b border-hairline px-5 py-4 pr-12 text-left">
          <div className="tw-eyebrow">报告预览</div>
          <SheetTitle className="tw-break font-mono text-[15px] leading-6">{report?.id}</SheetTitle>
          {report && (
            <>
              <div className="flex flex-wrap items-center gap-2">
                <StatusIndicator compact status={statusKind(report.status)} label={statusLabel(report.status)} />
                <Badge size="sm" variant="secondary">
                  <span className="tw-nums font-mono">{report.findings}</span> 条结论
                </Badge>
              </div>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px] leading-4 text-ink-3">
                <span className="tw-nums">生成 {fmtTime(report.created_at)}</span>
                {report.finished_at && <span className="tw-nums">完成 {fmtTime(report.finished_at)}</span>}
                <a
                  className="inline-flex items-center gap-1 text-ink-2 hover:text-primary"
                  href={api.reportFileURL(report.id, 'index.md')}
                  target="_blank"
                  rel="noreferrer"
                >
                  <Download className="h-3 w-3" /> 下载 index.md
                </a>
              </div>
            </>
          )}
        </SheetHeader>
        <ScrollArea className="min-h-0 flex-1">
          <div className="px-5 py-4">
            {doc.loading && <SkeletonList count={6} />}
            {doc.error && (
              <Callout tone="destructive">
                {doc.error}
                <Button size="xs" variant="outline" className="ml-2" onClick={doc.reload}>
                  重试
                </Button>
              </Callout>
            )}
            {doc.data && <MiniMarkdown text={doc.data} />}
          </div>
        </ScrollArea>
      </SheetContent>
    </Sheet>
  )
}
