import { useRef, useState } from 'react'
import { Check, LoaderCircle, Trash2 } from 'lucide-react'
import {
  Button, Callout, Dialog, DialogContent, DialogDescription,
  DialogFooter, DialogHeader, DialogTitle,
} from '@cyber/ui'
import { api } from '@/lib/api'
import type { ProjectInfo } from '@/lib/types'

/**
 * Destructive confirmation with a visible lifecycle: confirm → pending (no way
 * to double-submit or close early) → success / error (retry keeps the dialog in
 * place). The focus starts on Cancel, so a stray Enter never deletes anything.
 */
export function DeleteProjectDialog({ project, onClose, onDeleted }: {
  project: ProjectInfo
  onClose: () => void
  onDeleted: (id: string) => void
}) {
  const [state, setState] = useState<'confirm' | 'pending' | 'success' | 'error'>('confirm')
  const [error, setError] = useState('')
  const inFlight = useRef(false)
  const pending = state === 'pending'
  const success = state === 'success'

  async function remove() {
    if (inFlight.current || success) return
    inFlight.current = true
    setState('pending')
    setError('')
    try {
      await api.deleteProject(project.id)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setState('error')
      inFlight.current = false
      return
    }
    setState('success')
    onDeleted(project.id)
  }

  return (
    <Dialog open onOpenChange={open => { if (!open && !pending) onClose() }}>
      <DialogContent
        className={`tw-operation-dialog max-w-[calc(100%_-_2rem)] rounded-xl sm:max-w-md ${pending ? '[&>button:last-child]:hidden' : ''}`}
        closeLabel="关闭"
        onEscapeKeyDown={event => { if (pending) event.preventDefault() }}
        onPointerDownOutside={event => { if (pending) event.preventDefault() }}
        onOpenAutoFocus={event => {
          // Destructive confirmation starts on Cancel, including keyboard use.
          event.preventDefault()
          document.getElementById('cancel-project-deletion')?.focus()
        }}
      >
        <DialogHeader>
          <div
            className={`mb-2 flex h-11 w-11 items-center justify-center rounded-full ${
              success ? 'tw-operation-success bg-success/10 text-success' : 'bg-destructive/10 text-destructive'
            }`}
            aria-hidden="true"
          >
            {pending ? <LoaderCircle className="h-5 w-5 motion-safe:animate-spin" /> : success ? <Check className="h-5 w-5" /> : <Trash2 className="h-5 w-5" />}
          </div>
          <DialogTitle aria-live="polite">{pending ? '正在删除项目…' : success ? '项目已删除' : `删除项目「${project.name}」`}</DialogTitle>
          <DialogDescription className="break-words leading-relaxed">
            {pending ? `正在停止「${project.name}」的运行并清理项目数据，请稍候。`
              : success ? `「${project.name}」已删除，项目列表已自动更新。`
              : '将永久删除项目的事实意图图、工作区、报告与运行日志，无法撤销。'}
          </DialogDescription>
        </DialogHeader>
        {pending ? (
          <div className="space-y-3" role="status">
            <div role="progressbar" aria-label="项目删除进度" aria-valuetext="正在处理，等待服务器确认" className="h-1.5 overflow-hidden rounded-full bg-surface-2">
              <div className="tw-progress-indeterminate h-full w-1/3 rounded-full bg-primary" />
            </div>
            <p className="text-[12px] leading-relaxed text-ink-3">运行收尾可能需要一些时间，完成后会自动更新。请保持此窗口打开。</p>
          </div>
        ) : (
          <p className="text-[12px] leading-relaxed text-ink-3">已镜像到漏洞库的条目与全局安装的共享工具会保留；项目工作区内的工具随项目一起删除。</p>
        )}
        {error && (
          <div role="alert">
            <Callout tone="destructive">删除失败：{error}</Callout>
          </div>
        )}
        <DialogFooter className="gap-2">
          {pending ? <Button disabled aria-busy="true" className="w-full">正在删除，请稍候…</Button>
            : success ? <Button onClick={onClose}>完成</Button>
            : <>
              <Button id="cancel-project-deletion" variant="outline" onClick={onClose}>取消</Button>
              <Button variant="destructive" onClick={() => void remove()}>{state === 'error' ? '重试删除' : '确认删除'}</Button>
            </>}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
