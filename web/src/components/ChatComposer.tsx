// The operator's one 下发 box: one sentence plus two explicit choices (时效 and
// 对象). The server maps the choices onto three primitives so the operator does
// not have to know them:
//   一次性 + 指挥官      -> durable one-shot message to the commander (wakes a round)
//   一次性 + 方向        -> durable one-shot message to that direction only
//   一次性 + 方向 + steer -> stop its live turn and continue with the note
//   持续   + 整个项目    -> project-scope instruction (all future dispatches)
//   持续   + 方向        -> instruction scoped to that direction subtree
// A one-shot hint is never an instruction and a persistent constraint is never a
// message; the receipt says which one happened and what it affects.
import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Button,
  Checkbox,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Spinner,
  Textarea,
  ToggleGroup,
  ToggleGroupItem,
} from '@cyber/ui'
import { readViewMemory, writeViewMemory } from '@/lib/view-memory'
import { useSessionState } from '@/lib/session-state'
import { api } from '@/lib/api'
import {
  canSteer,
  directiveErrorMessage,
  directiveFingerprint,
  directiveKeyFor,
  directiveReceipt,
  directiveRequest,
  oneShotDirections,
  persistentDirections,
  runningDirectionsHint,
  type DirectiveDraft,
} from '@/lib/directive'
import { statusLabel } from '@/lib/format'
import { usePoll } from '@/lib/hooks'
import type { DirectiveTarget } from '@/lib/types'

type ComposerProps = {
  onSent: () => void
  paused: boolean
  runningExecutors: number
  autoFocus?: boolean
}

function randomKey(): string {
  return Array.from(crypto.getRandomValues(new Uint32Array(4)), (n) =>
    n.toString(16).padStart(8, '0'),
  ).join('')
}

export function ChatComposer(props: ComposerProps) {
  // No location key here: the composer is mounted per page right column, and the
  // project switch already remounts the pages that host it.
  return <Composer {...props} />
}

function Composer({ onSent, paused, runningExecutors, autoFocus = false }: ComposerProps) {
  // 一次性 is the safe default: it reads once and is not inherited. Turning the
  // switch to 持续 changes the target default from 指挥官 to 整个项目.
  const [persistent, setPersistent] = useSessionState('composer.persistent', false)
  const [target, setTarget] = useSessionState<DirectiveTarget>('composer.target', 'commander')
  const [intentID, setIntentID] = useSessionState('composer.intentID', '')
  const [steer, setSteer] = useState(false)
  const [text, setText] = useSessionState('composer.text', '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [receipt, setReceipt] = useState('')

  const intents = usePoll(api.intents, 8000)
  // One-shot may only reach a direction that is not settled; a persistent
  // instruction may scope to any direction (it applies to later descendants too).
  const directions = useMemo(
    () =>
      persistent
        ? persistentDirections(intents.data ?? [])
        : oneShotDirections(intents.data ?? []),
    [persistent, intents.data],
  )
  const selected = target === 'intent' ? directions.find((i) => i.id === intentID) : undefined
  const steerAllowed = !persistent && target === 'intent' && canSteer(selected)
  const steerActive = steer && steerAllowed

  // A selected direction can become settled while the poll refreshes (or while
  // switching 续效); drop the stale selection instead of sending to it.
  useEffect(() => {
    if (!intents.data || intents.error || target !== 'intent' || !intentID) return
    if (!directions.some((i) => i.id === intentID)) setIntentID('')
  }, [target, intentID, directions, intents.data, intents.error])

  // One key per directive: a transport retry of the same content/choices must
  // not create a second row; changing anything regenerates the key.
  const [savedKey, setSavedKey] = useSessionState('composer.request', { fingerprint: '', key: '' })
  const keyRef = useRef(savedKey)
  const draftScope = `draft:${window.location.pathname}`
  const active = useRef(true)
  useEffect(() => {
    active.current = true
    return () => {
      active.current = false
    }
  }, [])

  function choosePersistent(next: boolean) {
    if (busy) return
    setPersistent(next)
    // Keep the target valid for the chosen lifetime.
    setTarget((current) => {
      if (next && current === 'commander') return 'project'
      if (!next && current === 'project') return 'commander'
      return current
    })
    setSteer(false)
    setError('')
    setReceipt('')
  }

  function chooseTarget(next: DirectiveTarget) {
    if (busy) return
    setTarget(next)
    setSteer(false)
    setError('')
    setReceipt('')
  }

  function hint(): string {
    if (persistent) {
      if (target === 'project') {
        return '持续到撤销：影响之后新建的所有方向；暂停期间先保存，恢复后生效。'
      }
      return `持续到撤销：${selected?.title || '该方向'} 及其后代任务会继承，直到撤销。`
    }
    if (target === 'commander') {
      return paused
        ? '项目已暂停：一次性提示会排队，点「继续项目」后由指挥官的下一轮读取。'
        : `只影响指挥官下一轮决策，不建立持久指令；${runningDirectionsHint(runningExecutors)}。`
    }
    if (steerActive) {
      return '会停掉当前 run，用同一轨迹开新 turn；已写出的证据保留，不建立持久指令。'
    }
    if (selected) {
      return `一次性提示只发给这个方向（${statusLabel(selected.status)}）：正在跑就插话，否则排到它下一轮；不进其他方向，也不进后续提示词。`
    }
    return '一次性提示只发给选中的方向；不建立持久指令。'
  }

  async function send() {
    const content = text.trim()
    if (!content || busy) return
    if (target === 'intent' && !intentID) {
      setError('先选一个方向再下发')
      return
    }
    const draft: DirectiveDraft = {
      text: content,
      persistent,
      target,
      intentID: target === 'intent' ? intentID : '',
      steer: steerActive,
    }
    keyRef.current = directiveKeyFor(keyRef.current, directiveFingerprint(draft), randomKey)
    setSavedKey(keyRef.current)
    setBusy(true)
    setError('')
    setReceipt('')
    try {
      const result = await api.directive(directiveRequest(draft, keyRef.current.key))
      if (!active.current) {
        // A successful response may arrive after navigating away. Only clear
        // this submission, never a newer draft written in the other view.
        const stored = readViewMemory<{ key: string }>(draftScope, 'composer.request')
        if (stored?.key === keyRef.current.key &&
            readViewMemory<string>(draftScope, 'composer.text')?.trim() === content &&
            (readViewMemory<string>(draftScope, 'composer.target') ?? 'commander') === target &&
            (readViewMemory<boolean>(draftScope, 'composer.persistent') ?? false) === persistent &&
            (target !== 'intent' || readViewMemory<string>(draftScope, 'composer.intentID') === intentID)) {
          writeViewMemory(draftScope, 'composer.text', '')
        }
        return
      }
      setReceipt(directiveReceipt(result))
      // Only a successful server answer clears the draft: never optimistic.
      setText('')
      setSteer(false)
      keyRef.current = { fingerprint: '', key: '' }
      setSavedKey(keyRef.current)
      onSent()
    } catch (e) {
      if (active.current) setError(directiveErrorMessage(e))
    } finally {
      if (active.current) setBusy(false)
    }
  }

  return (
    <div className="shrink-0 space-y-2 border-t border-hairline p-3" data-directive-composer>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[12px] font-semibold text-ink">下发</span>
        <span className="text-[11px] text-ink-3">一次性提示 / 持续指令 / 换向</span>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <span className="w-8 shrink-0 text-[11px] text-ink-3">时效</span>
        <ToggleGroup
          value={persistent ? 'persist' : 'once'}
          onValueChange={(value) => choosePersistent(value === 'persist')}
          ariaLabel="下发时效"
        >
          <ToggleGroupItem value="once" disabled={busy} className="h-8 px-2 text-[11px]">
            一次性（默认）
          </ToggleGroupItem>
          <ToggleGroupItem value="persist" disabled={busy} className="h-8 px-2 text-[11px]">
            持续到撤销
          </ToggleGroupItem>
        </ToggleGroup>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <span className="w-8 shrink-0 text-[11px] text-ink-3">对象</span>
        <ToggleGroup value={target} onValueChange={(value) => chooseTarget(value as DirectiveTarget)} ariaLabel="下发对象">
          {persistent ? (
            <>
              <ToggleGroupItem value="project" disabled={busy} className="h-8 px-2 text-[11px]">
                整个项目
              </ToggleGroupItem>
              <ToggleGroupItem value="intent" disabled={busy} className="h-8 px-2 text-[11px]">
                某个方向
              </ToggleGroupItem>
            </>
          ) : (
            <>
              <ToggleGroupItem value="commander" disabled={busy} className="h-8 px-2 text-[11px]">
                指挥官
              </ToggleGroupItem>
              <ToggleGroupItem value="intent" disabled={busy} className="h-8 px-2 text-[11px]">
                某个方向
              </ToggleGroupItem>
            </>
          )}
        </ToggleGroup>
      </div>

      {target === 'intent' && (
        <div className="space-y-1.5">
          <Select
            value={intentID || undefined}
            onValueChange={(value) => {
              setIntentID(value)
              setSteer(false)
            }}
            disabled={busy || directions.length === 0}
          >
            <SelectTrigger className="h-8 text-[12px]" aria-label="选择下发方向">
              <SelectValue placeholder={directions.length > 0 ? '选择方向' : '没有可选方向'} />
            </SelectTrigger>
            <SelectContent className="max-w-[calc(100vw-2rem)]">
              {directions.map((intent) => (
                <SelectItem key={intent.id} value={intent.id} className="text-[12px]">
                  <span className="truncate">{intent.title}</span>
                  <span className="ml-1.5 font-mono text-[11px] text-ink-3">
                    {intent.id.slice(0, 12)}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {selected && (
            <div className="flex flex-wrap items-center gap-x-2 text-[11px] text-ink-3">
              <span className="font-mono">{selected.id}</span>
              <span>{statusLabel(selected.status)}</span>
            </div>
          )}
          {steerAllowed && (
            <label className="flex items-start gap-2 text-[11px] text-ink-2">
              <Checkbox
                checked={steerActive}
                onCheckedChange={(checked) => setSteer(Boolean(checked))}
                disabled={busy}
                aria-label="立即停并换向"
                className="mt-0.5"
              />
              <span>
                立即停并换向（steer）
                <span className="block text-[11px] leading-4 text-ink-3">
                  停掉当前 run，用同一轨迹继续；不勾选则作为一次性提示排到下一轮。
                </span>
              </span>
            </label>
          )}
        </div>
      )}

      <Textarea
        rows={2}
        value={text}
        disabled={busy}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault()
            void send()
          }
        }}
        maxLength={4000}
        autoFocus={autoFocus}
        placeholder={
          persistent
            ? target === 'project'
              ? '写持续有效的边界或要求：会影响之后新建的所有方向，直到撤销。'
              : '写这个方向及其后代都适用的边界或要求，直到撤销。'
            : target === 'commander'
              ? '例如：下一步先补 /api 的授权模型；拿到账号或白名单 IP 再继续，别在授权外试探。'
              : steerActive
                ? '写清换向后的下一步：例如停掉字典铺路，收窄到登录口做单变量差分。'
                : '例如：这个方向先停在只读验证；portal.example 主站也在范围内。'
        }
        className="min-h-[56px] text-[12px] leading-relaxed placeholder:text-ink-2"
        aria-label="下发内容"
      />
      <div className="flex items-center gap-2">
        <span className="tw-nums font-mono text-[11px] text-ink-3">
          {text.trim().length}/4000 · Enter 发送，Shift+Enter 换行
        </span>
        <Button
          size="sm"
          className="ml-auto shrink-0"
          disabled={busy || !text.trim() || (target === 'intent' && !intentID)}
          onClick={() => void send()}
        >
          {busy && <Spinner size="sm" className="text-primary-foreground" />}
          {busy ? '处理中…' : persistent ? '创建指令' : steerActive ? '停并换向' : '发送提示'}
        </Button>
      </div>
      <div className="text-[11px] leading-4 text-ink-3">{hint()}</div>
      {error && (
        <div role="alert" className="text-[11px] leading-4 text-destructive">
          {error}
        </div>
      )}
      {receipt && (
        <p data-directive-receipt className="text-[12px] leading-5 text-ink-2">
          {receipt}
        </p>
      )}
    </div>
  )
}
