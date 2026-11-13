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
import { Page } from '@/components/Page'
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
      setNote('已排入生成队列：一次真实的执行器运行，写完才出现在列表里。')
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
      {(generating || pendingReport) && <Callout className="mb-3"><span role="status">{generating ? '报告生成中，完成后会自动出现在下方。' : '已排队，正在等待报告运行。'}</span><a className="ml-2 underline" href="?view=runs">查看运行进度</a></Callout>}
      {pending && !generating && progress.data && !progress.error && !runs.error && !list.error && <p className="mb-3 text-xs text-muted-foreground">若等待没有进展，可先查看运行记录。<Button size="xs" variant="ghost" onClick={() => { setPendingReport(''); setNote('已结束等待；如需重新生成，请先核对运行记录。') }}>已核对，结束等待</Button></p>}
      {failure && <Callout tone="destructive" className="mb-3">{failure}</Callout>}
      {(list.error || progress.error || runs.error) && <Callout tone="warning" className="mb-3">状态更新失败，当前显示上次读取的数据。<Button size="xs" variant="outline" disabled={list.refreshing || progress.refreshing || runs.refreshing} onClick={() => { list.reload(); progress.reload(); runs.reload() }}>重试更新</Button></Callout>}
      {note && <Callout tone="info" className="mb-3">{note}</Callout>}
      {list.loading && <SkeletonList count={4} />}
      {!list.loading && !list.error && !generating && !pendingReport && rows.length === 0 && (
        <EmptyState
          icon={FileText}
          title="还没有报告"
          description="生成前会提示未复核结论；可先完成复核，或确认后继续生成报告。"
        />
      )}

      <div className="space-y-1.5">
        {rows.map((r) => (
          <div key={r.id} className="rounded-md border border-border bg-card px-3 py-2">
            <div className="flex flex-wrap items-center gap-2">
              <StatusIndicator compact status={statusKind(r.status)} label={statusLabel(r.status)} />
              <button
                type="button"
                className="font-mono text-[13px] text-foreground hover:text-primary"
                onClick={() => setOpen(r)}
              >
                {r.id}
              </button>
              <Badge size="sm" variant="secondary">
                {r.findings} 条结论
              </Badge>
              <span className="font-mono text-[11px] text-muted-foreground">
                {fmtTime(r.created_at)}
                {r.finished_at ? ` → ${fmtTime(r.finished_at)}` : ''}
              </span>
              <a
                className="ml-auto flex items-center gap-1 text-[12px] text-muted-foreground hover:text-primary"
                href={api.reportFileURL(r.id, 'index.md')}
                target="_blank"
                rel="noreferrer"
              >
                <Download className="h-3 w-3" /> index.md
              </a>
            </div>
            {r.checks && r.checks.length > 0 && (
              <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5">
                {r.checks.map((c, i) => (
                  <span
                    key={i}
                    title={c.detail}
                    className={`flex items-center gap-1 text-[11px] ${c.ok ? 'text-success' : 'text-destructive'}`}
                  >
                    {c.ok ? <CheckCircle2 className="h-3 w-3" /> : <XCircle className="h-3 w-3" />}
                    {c.rule}
                  </span>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>

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
      <SheetContent side="right" className="flex w-full flex-col gap-2 sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle className="font-mono text-ui">{report?.id}</SheetTitle>
        </SheetHeader>
        <ScrollArea className="min-h-0 flex-1 pr-2">
          {doc.loading && <SkeletonList count={6} />}
          {doc.error && <Callout tone="destructive">{doc.error}</Callout>}
          {doc.data && <MiniMarkdown text={doc.data} />}
        </ScrollArea>
      </SheetContent>
    </Sheet>
  )
}
