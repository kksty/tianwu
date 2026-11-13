import { useState } from 'react'
import { FolderOpen, LoaderCircle, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react'
import {
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Field,
  SkeletonList,
  Textarea,
} from '@cyber/ui'
import { Page } from '@/components/Page'
import { NewProjectDialog } from '@/components/NewProjectDialog'
import { DeleteProjectDialog } from '@/components/DeleteProjectDialog'
import { api, gotoProject } from '@/lib/api'
import { fmtTime } from '@/lib/format'
import type { ProjectInfo } from '@/lib/types'

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
      actions={
        <>
          <Button size="icon-sm" variant="ghost" onClick={onChanged} disabled={loading || refreshing} aria-label="刷新项目列表" aria-busy={refreshing} title="重新拉取项目列表">
            <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? 'motion-safe:animate-spin' : ''}`} />
          </Button>
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="h-3.5 w-3.5" /> 新建项目
          </Button>
        </>
      }
    >
      {loading ? <SkeletonList count={3} /> : <div className="overflow-x-auto rounded-md border border-border">
        <table className="w-full text-left text-[12px]">
          <thead className="bg-muted/50 text-[11px] uppercase tracking-wider text-muted-foreground">
            <tr>
              <th className="px-3 py-2 font-medium">项目</th>
              <th className="px-3 py-2 font-medium">授权边界</th>
              <th className="px-3 py-2 text-right font-medium">意图</th>
              <th className="px-3 py-2 text-right font-medium">事实</th>
              <th className="px-3 py-2 text-right font-medium">证据</th>
              <th className="px-3 py-2 text-right font-medium" title="同时最多跑几个意图；覆盖值在「设置 → 项目并发」">
                并发
              </th>
              <th className="px-3 py-2 font-medium">创建</th>
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody>
            {projects.map((p) => (
              <tr
                key={p.id}
                className={`border-t border-border/60 ${p.id === current ? 'bg-primary/5' : 'hover:bg-accent/30'}`}
              >
                <td className="px-3 py-2">
                  <div className="flex items-center gap-2">
                    <span className="text-ui text-foreground">{p.name}</span>
                    {p.id === current && <span className="h-1.5 w-1.5 rounded-full bg-primary" />}
                  </div>
                  <div className="font-mono text-[11px] text-muted-foreground">{p.id}</div>
                </td>
                <td className="tw-break max-w-[260px] px-3 py-2 font-mono text-muted-foreground">
                  {p.scope || '未指定'}
                </td>
                <td className="px-3 py-2 text-right font-mono">{p.intents}</td>
                <td className="px-3 py-2 text-right font-mono">{p.facts}</td>
                <td className="px-3 py-2 text-right font-mono">{p.evidence}</td>
                <td
                  className="px-3 py-2 text-right font-mono"
                  title={p.max_runs == null ? `继承实例默认（${p.max_runs_effective}）` : '项目覆盖'}
                >
                  {p.max_runs_effective === 0 ? '不限' : p.max_runs_effective}
                </td>
                <td className="px-3 py-2 font-mono text-muted-foreground">{fmtTime(p.created)}</td>
                <td className="px-3 py-2">
                  <div className="flex items-center justify-end gap-1">
                    {p.id !== current && (
                      <Button size="xs" variant="soft" onClick={() => gotoProject(p.id)}>
                        <FolderOpen className="h-3 w-3" /> 打开
                      </Button>
                    )}
                    <Button size="icon-xs" variant="ghost" onClick={() => openScope(p)} aria-label={`编辑项目「${p.name}」`} title="改授权边界（当前口径）">
                      <Pencil className="h-3 w-3" />
                    </Button>
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      className="text-muted-foreground hover:text-destructive"
                      onClick={() => setDeleting(p)}
                      aria-label={`删除项目「${p.name}」`}
                      title="删除项目"
                    >
                      <Trash2 className="h-3 w-3" />
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>}

      {!loading && projects.length === 0 && (
        <Callout tone="info" className="mt-3">
          还没有项目。点右上角「新建项目」：一个渗透对象一套事实意图图。
        </Callout>
      )}

      <NewProjectDialog open={creating} onOpenChange={setCreating} onCreated={onChanged} />
      {deleting && <DeleteProjectDialog project={deleting} onClose={() => setDeleting(null)} onDeleted={deleted} />}

      <Dialog open={!!editing} onOpenChange={(o) => { if (!o && !busy) setEditing(null) }}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>授权边界 · {editing?.name}</DialogTitle>
          </DialogHeader>
          <Field
            label="当前口径（可动态补充）"
            hint="写入派发简报和指挥官决策；资产动态增删时用图谱右侧下发窗口补充即可，不必改这里。程序不解析、不逐资产校验。"
          >
            <Textarea
              rows={3}
              value={scope}
              disabled={busy}
              onChange={(e) => setScope(e.target.value)}
              className="font-mono text-xs"
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
              className="text-xs"
            />
          </Field>
          {error && <div className="mt-2 text-[12px] text-destructive">{error}</div>}
          <DialogFooter>
            <Button variant="outline" disabled={busy} onClick={() => setEditing(null)}>
              取消
            </Button>
            <Button onClick={saveScope} disabled={busy} aria-busy={busy}>
              {busy && <LoaderCircle className="mr-1.5 h-3.5 w-3.5 motion-safe:animate-spin" />}{busy ? '保存中…' : '保存'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Page>
  )
}
