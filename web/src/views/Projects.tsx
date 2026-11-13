import { useState } from 'react'
import { FolderKanban, FolderOpen, Pencil, Plus, RefreshCw, Trash2, TriangleAlert } from 'lucide-react'
import { cn } from '@cyber/theme'
import {
  Badge,
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  EmptyState,
  Field,
  SkeletonList,
  Spinner,
  Textarea,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { NewProjectDialog } from '@/components/NewProjectDialog'
import { DeleteProjectDialog } from '@/components/DeleteProjectDialog'
import { api, gotoProject } from '@/lib/api'
import { fmtInt, fmtTime } from '@/lib/format'
import type { ProjectInfo } from '@/lib/types'

/** One number in the row's right-hand metric grid: quiet label over mono value. */
function RowMetric({ label, value, title }: { label: string; value: string; title?: string }) {
  return (
    <div className="min-w-0 text-right" title={title}>
      <div className="truncate text-[11px] leading-4 text-ink-3">{label}</div>
      <div className="tw-nums mt-0.5 truncate font-mono text-[12px] leading-4 text-ink">{value}</div>
    </div>
  )
}

export function Projects({
  projects,
  current,
  onChanged,
  onDeleted,
  refreshing,
  loading,
}: {
  projects: ProjectInfo[]
  current: string
  onChanged: () => void
  onDeleted: (id: string) => void
  refreshing: boolean
  loading: boolean
}) {
  const [editing, setEditing] = useState<ProjectInfo | null>(null)
  const [scope, setScope] = useState('')
  const [description, setDescription] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<ProjectInfo | null>(null)

  function openScope(p: ProjectInfo) {
    setEditing(p)
    setScope(p.scope ?? '')
    setDescription(p.description ?? '')
    setError('')
  }

  async function saveScope() {
    if (!editing || busy) return
    setError('')
    setBusy(true)
    try {
      await api.updateProject(editing.id, { scope: scope.trim(), description: description.trim() })
      setEditing(null)
      onChanged()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  function deleted(id: string) {
    onDeleted(id)
    if (id === current) {
      const next = projects.find(p => p.id !== id)
      if (next) gotoProject(next.id)
      else window.location.assign('/g/?view=projects')
    }
  }

  return (
    <Page
      title="项目"
      subtitle="一个项目 = 一个渗透对象 = 一套事实意图图；工具、模板、报告体例实例级共享"
      meta={
        <span className="tw-eyebrow normal-case tracking-normal">
          共 <span className="tw-nums font-mono">{fmtInt(projects.length)}</span> 个项目
        </span>
      }
      actions={
        <>
          <Button
            size="icon-sm"
            variant="ghost"
            onClick={onChanged}
            disabled={loading || refreshing}
            aria-label="刷新项目列表"
            aria-busy={refreshing}
            title="重新拉取项目列表"
          >
            <RefreshCw className={cn('h-3.5 w-3.5', refreshing && 'motion-safe:animate-spin')} />
          </Button>
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="h-3.5 w-3.5" /> 新建项目
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Panel
          title="项目列表"
          extra={
            <span className="flex items-center gap-2 text-[11px] text-ink-3">
              <span>{loading ? '读取中…' : refreshing ? '刷新中…' : `${fmtInt(projects.length)} 个`}</span>
            </span>
          }
          bodyClassName="p-0"
        >
          {loading && projects.length === 0 ? (
            <div className="p-4">
              <SkeletonList count={4} />
            </div>
          ) : projects.length === 0 ? (
            <EmptyState
              icon={FolderKanban}
              title="还没有项目"
              description="项目是一个渗透对象的事实意图图。新建后即可派发意图、积累事实与证据。"
              action={
                <Button size="sm" variant="outline" onClick={() => setCreating(true)}>
                  <Plus className="h-3.5 w-3.5" /> 新建项目
                </Button>
              }
            />
          ) : (
            <div className="divide-y divide-hairline">
              {projects.map((p) => {
                const open = p.id === current
                return (
                  <div
                    key={p.id}
                    className={cn(
                      'group flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3 transition-colors',
                      'hover:bg-surface-2/60',
                      open && 'bg-primary/[0.06] shadow-[inset_2px_0_0_var(--primary)]',
                    )}
                  >
                    <div className="flex min-w-0 flex-1 items-start gap-2.5">
                      <span
                        className={cn(
                          'mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-lg border',
                          open ? 'border-primary/25 bg-primary/10 text-primary' : 'border-hairline bg-surface-2 text-ink-3',
                        )}
                        aria-hidden="true"
                      >
                        <FolderKanban className="h-4 w-4" />
                      </span>
                      <div className="min-w-0">
                        <div className="flex min-w-0 items-center gap-2">
                          <span className="truncate text-[13px] font-medium leading-5 text-ink">{p.name}</span>
                          {open && (
                            <Badge variant="primary" size="sm" className="shrink-0">
                              当前
                            </Badge>
                          )}
                        </div>
                        <div className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] leading-4 text-ink-3">
                          <span className="font-mono">{p.id}</span>
                          <span className="hidden text-ink-3/50 sm:inline">·</span>
                          <span className="hidden tw-break font-mono sm:inline">{p.scope || '未指定授权边界'}</span>
                          <span className="md:hidden">
                            意图 <span className="tw-nums font-mono text-ink-2">{fmtInt(p.intents)}</span> · 事实{' '}
                            <span className="tw-nums font-mono text-ink-2">{fmtInt(p.facts)}</span> · 证据{' '}
                            <span className="tw-nums font-mono text-ink-2">{fmtInt(p.evidence)}</span>
                          </span>
                        </div>
                      </div>
                    </div>

                    <div className="hidden w-[280px] shrink-0 grid-cols-4 gap-3 md:grid">
                      <RowMetric label="意图" value={fmtInt(p.intents)} />
                      <RowMetric label="事实" value={fmtInt(p.facts)} />
                      <RowMetric label="证据" value={fmtInt(p.evidence)} />
                      <RowMetric
                        label="并发"
                        value={p.max_runs_effective === 0 ? '不限' : fmtInt(p.max_runs_effective)}
                        title={p.max_runs == null ? `继承实例默认（${p.max_runs_effective}）` : '项目覆盖'}
                      />
                    </div>

                    <span className="hidden w-[96px] shrink-0 text-right font-mono text-[11px] text-ink-3 lg:block">
                      {fmtTime(p.created)}
                    </span>

                    <div className="flex shrink-0 items-center gap-1">
                      {!open && (
                        <Button size="xs" variant="outline" onClick={() => gotoProject(p.id)}>
                          <FolderOpen className="h-3.5 w-3.5" /> 打开
                        </Button>
                      )}
                      <Button
                        size="icon-xs"
                        variant="ghost"
                        onClick={() => openScope(p)}
                        aria-label={`编辑项目「${p.name}」`}
                        title="改授权边界（当前口径）"
                      >
                        <Pencil className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        size="icon-xs"
                        variant="ghost"
                        className="text-ink-2 hover:bg-destructive/10 hover:text-destructive"
                        onClick={() => setDeleting(p)}
                        aria-label={`删除项目「${p.name}」`}
                        title="删除项目"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </Panel>

        <p className="px-1 text-[11px] leading-4 text-ink-3">
          删除项目会停止其运行并清理事实意图图、工作区与运行日志；删除前会二次确认。已镜像到漏洞库的条目与全局工具不受影响。
        </p>
      </div>

      <NewProjectDialog open={creating} onOpenChange={setCreating} onCreated={onChanged} />
      {deleting && <DeleteProjectDialog project={deleting} onClose={() => setDeleting(null)} onDeleted={deleted} />}

      <Dialog open={!!editing} onOpenChange={(o) => { if (!o && !busy) setEditing(null) }}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>授权边界 · {editing?.name}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <Field
              label="当前口径（可动态补充）"
              hint="写入派发简报和指挥官决策；资产动态增删时用图谱右侧下发窗口补充即可，不必改这里。程序不解析、不逐资产校验。"
            >
              <Textarea
                rows={3}
                value={scope}
                disabled={busy}
                onChange={(e) => setScope(e.target.value)}
                className="font-mono text-[12px]"
              />
            </Field>
            <Field
              label="项目描述（可选）"
              hint="平台规则、禁止动作、目标材料口径；原样带进简报和决策，不解析、不校验。"
            >
              <Textarea
                rows={2}
                value={description}
                disabled={busy}
                onChange={(e) => setDescription(e.target.value)}
                className="text-[12px]"
              />
            </Field>
            {error && (
              <div role="alert">
                <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
                  保存失败：{error}
                </Callout>
              </div>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" disabled={busy} onClick={() => setEditing(null)}>
              取消
            </Button>
            <Button onClick={saveScope} disabled={busy} aria-busy={busy}>
              {busy && <Spinner size="sm" className="h-3.5 w-3.5" />}
              {busy ? '保存中…' : '保存'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Page>
  )
}
