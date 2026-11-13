import { useRef, useState } from 'react'
import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Field,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Textarea,
} from '@cyber/ui'
import { useSessionState } from '@/lib/session-state'
import { navigate } from '@/lib/navigation'
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'

/** The one way to add work: an intent is the unit the executor receives. */
export function CreateIntentDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (id: string) => void
}) {
  const [title, setTitle] = useSessionState('intent.title', '')
  const [description, setDescription] = useSessionState('intent.description', '')
  const [profile, setProfile] = useSessionState('intent.profile', '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const nodes = useLoad(api.nodes, [open])
  const profiles = useLoad(api.modelProfiles, [open])
  const bound = (nodes.data?.nodes ?? []).filter((n) => n.bound)
  const runnable = (profiles.data?.profiles ?? []).filter((p) => p.runnable)
  const activeProfile = profiles.data?.active ?? ''

  const input = useRef<HTMLTextAreaElement>(null)
  const submitting = useRef(false)
  const [invalid, setInvalid] = useState(false)
  const checking = nodes.loading || nodes.refreshing || profiles.loading || profiles.refreshing
  const dependencyError = nodes.error || profiles.error
  const ready = !checking && !dependencyError && bound.length > 0 && (!profile || runnable.some(p => p.id === profile))

  async function submit() {
    if (submitting.current || !ready) return
    if (!title.trim() && !description.trim()) {
      setInvalid(true)
      input.current?.focus()
      return
    }
    submitting.current = true
    setBusy(true)
    setError('')
    try {
      const created = await api.createIntent({
        title: title.trim(),
        description: description.trim(),
        // Authorization and prohibitions live in the project brief + built-in
        // constraints; an intent does not carry its own scope document.
        node: undefined,
        profile: profile || undefined,
      })
      setTitle('')
      setDescription('')
      setProfile('')
      onOpenChange(false)
      onCreated(created.id)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      submitting.current = false
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!busy) onOpenChange(next) }}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto max-w-lg" onKeyDown={(e) => {
        if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && !e.nativeEvent.isComposing) { e.preventDefault(); void submit() }
      }}>
        <DialogHeader>
          <DialogTitle>新建意图</DialogTitle>
          <DialogDescription>
            把要验证的问题写清楚即可。授权范围与禁止事项由项目简报和内置约束负责；新方向由指挥官/执行者自行划分，不必在这里设置测试范围。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <Field label="标题（可选）" hint="留空则自动取问题的第一句">
            <Input
              disabled={busy}
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="例如：验证 /admin 越权读取订单"
            />
          </Field>
          <Field error={invalid && !title.trim() && !description.trim() ? "标题或问题至少填一个" : undefined} label="要验证什么" hint="一句话说清要验证的问题；只说清目标，不写授权边界">
            <Textarea
              ref={input}
              autoFocus
              disabled={busy}
              aria-invalid={invalid && !title.trim() && !description.trim()}
              rows={4}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="普通用户能否直接读取他人订单？"
            />
          </Field>
          {runnable.length > 0 && (
            <Field
              label="模型配置"
              hint="不选就用实例默认；选中的配置会以独立本机宿主运行，只有本机节点能执行"
            >
              <Select disabled={busy} value={profile || '__default'} onValueChange={(v) => setProfile(v === '__default' ? '' : v)}>
                <SelectTrigger className="h-9 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__default" className="text-xs">
                    实例默认{activeProfile ? `（${activeProfile}）` : ''}
                  </SelectItem>
                  {runnable.map((p) => (
                    <SelectItem key={p.id} value={p.id} className="text-xs">
                      <span className="font-mono">{p.id}</span>
                      {p.model ? <span className="ml-2 text-[11px] text-ink-3">{p.model}</span> : null}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          {checking ? <p role="status" className="text-xs text-ink-3">正在检查执行节点与模型配置…</p> : dependencyError ? (
            <div role="alert" className="space-y-2 text-xs text-destructive">
              <p>暂时无法检查执行环境，输入已保留。请重试。</p>
              <Button size="xs" variant="outline" onClick={() => { nodes.reload(); profiles.reload() }}>重新检查</Button>
              <details><summary>错误详情</summary>{dependencyError}</details>
            </div>
          ) : profile && !runnable.some(p => p.id === profile) ? <p role="alert" className="text-xs text-destructive">原模型配置已不可用，请选择实例默认或其他配置。<Button size="xs" variant="outline" onClick={() => setProfile('')}>使用实例默认</Button></p> : bound.length === 0 ? (
            <div className="space-y-2 text-xs"><p>尚未绑定执行节点，请检查本机宿主。</p><Button size="xs" variant="outline" onClick={() => { onOpenChange(false); navigate(`${window.location.pathname}?view=runtime&tab=nodes`) }}>打开执行环境</Button></div>
          ) : profile ? (
            <div className="rounded-md border border-hairline bg-surface-2 px-3 py-2 text-[12px] text-ink-3">
              已指定模型配置 <span className="font-mono">{profile}</span>：本次运行仍在本机，宿主按该配置启动。
            </div>
          ) : (
            <div className="text-[12px] text-ink-3">
              执行节点：{bound[0]?.id}（单机实例，自动绑定本机宿主）
            </div>
          )}
          {error && <div role="alert" className="text-[12px] text-destructive">创建失败，输入已保留，可重试。<details><summary>错误详情</summary>{error}</details></div>}
        </div>
        <p className="text-xs text-ink-3">Ctrl / ⌘ + Enter 创建 · 草稿会在当前标签页保留</p>
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy || !ready}>
            {busy ? '创建中…' : '创建'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
