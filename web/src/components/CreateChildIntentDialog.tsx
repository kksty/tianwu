import { useEffect, useState } from 'react'
import {
  Button,
  Callout,
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
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'
import type { CreateChildIntentResult } from '@/lib/types'

/** What the child hangs under: the direction currently open in the inspector. */
export interface ChildIntentParent {
  id: string
  title: string
}

/** Cryptographically random when available; readable and unique enough otherwise. */
function newChildKey(): string {
  const rand =
    typeof crypto !== 'undefined' && 'randomUUID' in crypto
      ? crypto.randomUUID()
      : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
  return `ui-child-${rand}`
}

/**
 * Hang one piece of work under the direction in the inspector. This is an
 * optional split, not a required step: the parent keeps running, the child gets
 * its own run (and its own profile), and both are ordinary intent rows.
 *
 * The form is just the task input. How the work gets done stays the executor's
 * business; the console does not prescribe an attack order here.
 */
export function CreateChildIntentDialog({
  open,
  onOpenChange,
  parent,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  parent: ChildIntentParent
  /** Called after the server accepted the child; the inspector reloads its list. */
  onCreated: (r: CreateChildIntentResult) => void
}) {
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [evalText, setEvalText] = useState('')
  const [profile, setProfile] = useState('')
  const [key, setKey] = useState(() => newChildKey())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<CreateChildIntentResult | null>(null)
  const profiles = useLoad(api.modelProfiles, [open])
  const runnable = (profiles.data?.profiles ?? []).filter((p) => p.runnable)

  // A fresh open is a fresh task: clear the fields and mint the key this dialog
  // keeps for its whole life. A retry after a failure reuses it on purpose.
  useEffect(() => {
    if (!open) return
    setTitle('')
    setDescription('')
    setEvalText('')
    setProfile('')
    setKey(newChildKey())
    setBusy(false)
    setError('')
    setResult(null)
  }, [open, parent.id])

  async function submit() {
    const cleanTitle = title.trim()
    if (!cleanTitle) {
      setError('先写标题：子任务是账本里的一行')
      return
    }
    setBusy(true)
    setError('')
    try {
      const r = await api.createChildIntent(parent.id, {
        title: cleanTitle,
        description: description.trim() || undefined,
        eval: evalText.trim() || undefined,
        profile: profile || undefined,
        idempotency_key: key,
      })
      setResult(r)
      onCreated(r)
      // A newly created child consumed this key; another child is another task.
      // On an idempotent hit the key stays, because that is the same child.
      if (r.created) {
        setTitle('')
        setDescription('')
        setEvalText('')
        setProfile('')
        setKey(newChildKey())
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>派子意图</DialogTitle>
          <DialogDescription>
            把一块工作挂到「{parent.title}」下面（可选）：子任务有自己的 run，会自动开跑；
            父方向继续跑，子任务出现在子任务列表和意图图里。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <Field label="标题" hint="一句话说清这块工作要产出什么">
            <Input
              value={title}
              maxLength={200}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="例如：验证 /admin 越权读取订单"
            />
          </Field>
          <Field label="要回答的问题 / 任务" hint="执行者只看这段文字：目标、输入、边界">
            <Textarea
              rows={3}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="普通用户能否直接读取他人订单？只做只读验证，不改数据。"
            />
          </Field>
          <Field label="验收标准（可选）" hint="什么样的结果算答完；交给 harness 的目标评估器">
            <Textarea
              rows={2}
              value={evalText}
              onChange={(e) => setEvalText(e.target.value)}
              placeholder="例如：给出可复现的请求/响应，并说明是否越权"
            />
          </Field>
          {runnable.length > 0 && (
            <Field
              label="模型配置（可选）"
              hint="不选就使用项目执行者默认模型；同一种模型也可委派"
            >
              <Select
                value={profile || '__inherit'}
                onValueChange={(v) => setProfile(v === '__inherit' ? '' : v)}
              >
                <SelectTrigger className="h-9 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__inherit" className="text-xs">
                    使用项目执行者默认模型
                  </SelectItem>
                  {runnable.map((p) => (
                    <SelectItem key={p.id} value={p.id} className="text-xs">
                      <span className="font-mono">{p.id}</span>
                      {p.model ? (
                        <span className="ml-2 text-[11px] text-ink-3">{p.model}</span>
                      ) : null}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          <Field
            label="幂等键（自动生成）"
            hint="同一对话框内保持不变：重试用同一个键会找回这个子任务，不会重复创建"
          >
            <div className="tw-break font-mono text-[11px] text-ink-3">{key}</div>
          </Field>

          {error && <Callout tone="destructive">{error}</Callout>}

          {result && (
            <Callout tone={result.created ? 'success' : 'warning'}>
              <div className="space-y-0.5">
                {result.created ? (
                  <div>
                    子任务已创建{result.started ? '并开跑' : '，等并发空闲后自动开始'}：
                    <span className="ml-1 font-mono">{result.child.id}</span>
                  </div>
                ) : (
                  <div>
                    幂等命中：子任务 <span className="font-mono">{result.child.id}</span>{' '}
                    已存在，没有重复创建。
                  </div>
                )}
                {result.child.title && (
                  <div className="text-[11px] opacity-80">{result.child.title}</div>
                )}
                {result.queued && <div className="text-[11px] opacity-80">{result.queued}</div>}
              </div>
            </Callout>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {result ? '关闭' : '取消'}
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            {busy ? '创建中…' : '创建子任务'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
