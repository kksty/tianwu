import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ArrowRight, Check, FileKey2, Fingerprint, Loader2, Monitor, Plus, RefreshCw, Trash2 } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  EmptyState,
  Field,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SkeletonList,
  StatusIndicator,
  Textarea,
  useConfirm,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { BrowserOccupancy } from '@/components/BrowserOccupancy'
import { api } from '@/lib/api'
import { usePoll } from '@/lib/hooks'
import { createRequestGate } from '@/lib/requests'
import { canOperateBrowser } from '@/lib/access'
import type { AccessEntry, OperatorRequest } from '@/lib/types'

const LABEL = { browser: '浏览器现场', state: '登录状态文件', request: '已认证请求' }
const message = (e: unknown) => e instanceof Error ? e.message : String(e)
type Mode = 'cdp' | 'new' | 'state' | 'request'
/** Radix Select forbids an empty item value; this sentinel means "use a browser". */
const USE_BROWSER = '__browser'

/**
 * Access is two jobs on one page, read top to bottom:
 *   1. 待你处理 — live human handoffs (scan a QR code, solve a captcha). These are
 *      time-sensitive cards, each with its own completion form.
 *   2. 浏览器与已保存材料 — the durable resources. Each card separates "what the
 *      material is" (header) from "who is holding it" (the recessed occupancy
 *      strip) from "what I can do" (the action row).
 *
 * Every destructive action (delete a material) goes through the shared confirm
 * dialog with the destructive style.
 */
export function Access() {
  const list = usePoll(api.access, 4000)
  const waiting = usePoll(api.operatorRequests, 4000)
  const [adding, setAdding] = useState(false)
  const [context, setContext] = useState<OperatorRequest | null>(null)
  const [mode, setMode] = useState<Mode>('cdp')
  const [name, setName] = useState('')
  const [origin, setOrigin] = useState('')
  const [cdp, setCDP] = useState('')
  const [material, setMaterial] = useState('')
  const [reading, setReading] = useState(false)
  const [fileReads] = useState(() => createRequestGate<string>())
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [receipt, setReceipt] = useState('')
  const form = useRef<HTMLDivElement>(null)
  const confirm = useConfirm()
  const refresh = () => { list.reload(); waiting.reload() }
  useEffect(() => () => fileReads.invalidate(), [fileReads])
  function changeMode(next: Mode) {
    fileReads.invalidate(); setReading(false); setMaterial(''); setMode(next); setError('')
  }
  function prepare(item: OperatorRequest | null = null) {
    setContext(item); setAdding(true); setName(''); setOrigin(''); setCDP(''); setReceipt('')
    changeMode(item ? 'new' : 'cdp')
    requestAnimationFrame(() => form.current?.scrollIntoView({ behavior: 'smooth', block: 'center' }))
  }
  function readMaterial(file?: File) {
    if (!file) return
    void fileReads.run(async () => {
      if (file.size > 2 * 1024 * 1024) throw new Error('材料超过 2 MiB')
      return file.text()
    }, {
      start: () => { setMaterial(''); setError(''); setReading(true) }, value: setMaterial,
      error: e => setError(message(e)), finish: () => setReading(false),
    }, true)
  }
  async function openBrowser(entry: AccessEntry) {
    let lease = entry.lease
    if (!lease) lease = (await api.browserLease(entry.id, { action: 'acquire' })).lease
    if (!lease || !canOperateBrowser({ ...entry, lease })) throw new Error('浏览器正在由执行者使用，或需要先确认恢复')
    const result = await api.browserAction(entry.id, 'open', lease.id)
    setReceipt(`${entry.name}\n${result.output}`)
  }
  async function create(e: FormEvent) {
    e.preventDefault()
    if (reading || busy) return
    setBusy('create'); setError(''); setReceipt('')
    try {
      const browser = mode === 'cdp' || mode === 'new'
      const entry = await api.createAccess({ name: name.trim(), kind: browser ? 'browser' : mode,
        origin: mode === 'cdp' ? '' : origin.trim(), cdp_url: mode === 'cdp' ? cdp.trim() : '',
        ...(context ? { intent_id: context.intent_id, request_id: context.request.id } : {}),
        ...(!browser ? { material: JSON.parse(material) } : {}) })
      setAdding(false); setMaterial(''); setName(''); setCDP(''); setContext(null)
      // Keep successful registration if connecting fails, so retrying uses the
      // same resource instead of registering an external browser twice.
      if (browser) await openBrowser(entry)
    } catch (e) { setError(message(e)) } finally { setBusy(''); refresh() }
  }
  async function action(entry: AccessEntry, op: 'open' | 'inspect' | 'save' | 'detach') {
    if (busy) return
    setBusy(entry.id); setError(''); setReceipt('')
    try {
      if (op === 'open') await openBrowser(entry)
      else {
        if (!canOperateBrowser(entry) || !entry.lease) return
        const result = await api.browserAction(entry.id, op, entry.lease.id)
        setReceipt(`${entry.name}\n${result.output}`)
      }
    } catch (e) { setError(message(e)) } finally { setBusy(''); list.reload() }
  }
  async function changeLease(entry: AccessEntry, action: 'acquire' | 'release' | 'recover', note?: string) {
    if (busy) return false
    setBusy(entry.id); setError('')
    try {
      await api.browserLease(entry.id, { action, lease_id: entry.lease?.id, note, ...(action === 'recover' ? { confirmed_idle: true } : {}) })
      return true
    } catch (e) { setError(message(e)); return false }
    finally { setBusy(''); list.reload() }
  }
  async function remove(entry: AccessEntry) {
    if (!await confirm({
      title: '删除登录材料',
      description: `删除「${entry.name}」的项目登记，不会关闭外部浏览器或清除已发出的材料副本。`,
      destructive: true,
      confirmLabel: '删除材料',
      cancelLabel: '取消',
    })) return
    setBusy(entry.id); setError('')
    try { await api.deleteAccess(entry.id) } catch (e) { setError(message(e)) } finally { setBusy(''); list.reload() }
  }

  const pending = (waiting.data ?? []).filter(item => !item.request.completed_at)
  const completed = (waiting.data ?? []).some(item => item.request.completed_at)
  const browser = mode === 'cdp' || mode === 'new'
  const entries = list.data ?? []
  const refreshing = list.refreshing || waiting.refreshing

  return (
    <Page
      title="访问与交接"
      subtitle="执行者处理登录，需要扫码或验证码时在这里接手"
      actions={
        <Button
          size="sm"
          variant="outline"
          aria-label="刷新访问与交接"
          onClick={refresh}
          // A background poll's own "refreshing" must not swallow the manual
          // click; only a first load blocks it.
          disabled={list.loading || waiting.loading}
        >
          <RefreshCw className={refreshing ? 'h-3.5 w-3.5 animate-spin' : 'h-3.5 w-3.5'} /> 刷新
        </Button>
      }
    >
      <div className="mx-auto max-w-[1200px] space-y-5 pb-6">
        <Panel
          title={
            <span className="flex items-center gap-2">
              <Fingerprint className="h-4 w-4 text-ink-3" />
              扫码、验证码与人工确认
            </span>
          }
          extra={
            <Button size="sm" variant="outline" onClick={() => prepare()}>
              <Plus className="h-3.5 w-3.5" /> 连接浏览器
            </Button>
          }
        >
          <p className="text-[13px] leading-relaxed text-ink-2">
            用户名密码可直接通过意图或插话提供，执行者自行登录。需要人工操作时会带着浏览器现场来申请，完成后继续原任务。
          </p>
        </Panel>

        {(error || list.error || waiting.error) && (
          <div role="alert">
            <Callout tone="destructive">
              {error || list.error || waiting.error}
              <Button size="xs" variant="outline" className="ml-2" onClick={refresh}>
                重试
              </Button>
            </Callout>
          </div>
        )}

        <section aria-label="等待人工处理" className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="tw-section-title">待你处理</h2>
            <Badge size="sm" variant={pending.length > 0 ? 'warning' : 'muted'}>
              <span className="tw-nums font-mono">{pending.length}</span>
            </Badge>
            <span className="ml-auto text-[11px] leading-4 text-ink-3">
              需要扫码 / 验证码时，执行者会带着现场来申请
            </span>
          </div>
          {waiting.loading ? (
            <SkeletonList count={1} />
          ) : waiting.error && !waiting.data ? (
            <Callout tone="warning">
              暂时无法确认人工请求，请刷新重试。
            </Callout>
          ) : pending.length === 0 ? (
            <div className="tw-panel flex items-start gap-3 rounded-xl px-4 py-5">
              <Check className="mt-0.5 h-4 w-4 shrink-0 text-success" />
              <div className="min-w-0">
                <p className="text-[13px] font-medium leading-5 text-ink">当前没有人工操作请求</p>
                <p className="mt-0.5 text-[12px] leading-relaxed text-ink-2">
                  无需提前登录或准备材料，可以直接派发任务。
                </p>
              </div>
            </div>
          ) : (
            <div className="grid gap-3 xl:grid-cols-2">
              {pending.map(item => (
                <OperatorCard
                  key={item.request.id}
                  item={item}
                  entries={entries}
                  disabled={!!busy}
                  onPrepare={() => prepare(item)}
                  onComplete={refresh}
                />
              ))}
            </div>
          )}
          {completed && (
            <p className="text-[11px] leading-4 text-ink-3">
              人工回执已提交，等待原任务恢复，无需重复提交。
            </p>
          )}
        </section>

        {adding && (
          <div ref={form}>
            <Panel
              title={context ? `为「${context.title}」准备登录现场` : '连接浏览器'}
              extra={
                <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => setAdding(false)}>
                  收起
                </Button>
              }
            >
              <form onSubmit={create} className="space-y-4">
                <div className="flex flex-wrap gap-2" role="group" aria-label="连接方式">
                  {([['cdp', '连接已有浏览器'], ['new', '打开新浏览器']] as const).map(([value, label]) => (
                    <Button
                      key={value}
                      type="button"
                      size="sm"
                      disabled={!!busy}
                      variant={mode === value ? 'soft' : 'outline'}
                      active={mode === value}
                      onClick={() => changeMode(value)}
                    >
                      {label}
                    </Button>
                  ))}
                </div>
                <p className="text-[12px] leading-relaxed text-ink-3">
                  {context ? '关联这次人工请求，操作完成后回到原任务。' : '可直接连接，也可等待执行者申请后再处理。'}
                </p>
                {mode === 'cdp' ? (
                  <Field
                    label="CDP 地址"
                    hint="连接整个浏览器，可保留多个站点的标签页。浏览器需已开启本机调试端口。"
                  >
                    <Input
                      required
                      type="url"
                      placeholder="http://127.0.0.1:9222"
                      disabled={!!busy}
                      value={cdp}
                      onChange={e => setCDP(e.target.value)}
                      className="h-8 font-mono text-[12px]"
                    />
                  </Field>
                ) : (
                  <Field label="站点 / 登录页">
                    <Input
                      required
                      type="url"
                      placeholder="https://app.example/login"
                      disabled={!!busy}
                      value={origin}
                      onChange={e => setOrigin(e.target.value)}
                      className="h-8 font-mono text-[12px]"
                    />
                  </Field>
                )}
                <details className="rounded-lg border border-hairline bg-surface-2/40 p-3">
                  <summary className="cursor-pointer select-none text-[12px] text-ink-2 hover:text-ink">
                    更多：命名或导入已有材料
                  </summary>
                  <div className="mt-3 space-y-3 border-t border-hairline pt-3">
                    <Field label="名称（可选）" hint="默认自动生成；多账号时可用名称区分各自的浏览器。">
                      <Input
                        maxLength={100}
                        disabled={!!busy}
                        value={name}
                        onChange={e => setName(e.target.value)}
                        className="h-8 text-[12px]"
                      />
                    </Field>
                    <Field label="导入材料">
                      <Select
                        value={browser ? USE_BROWSER : mode}
                        onValueChange={v => changeMode((v === USE_BROWSER ? 'cdp' : v) as Mode)}
                        disabled={!!busy}
                      >
                        <SelectTrigger className="h-8 text-[12px]" aria-label="导入材料">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value={USE_BROWSER}>使用浏览器</SelectItem>
                          <SelectItem value="state">登录状态 JSON</SelectItem>
                          <SelectItem value="request">已认证请求 JSON</SelectItem>
                        </SelectContent>
                      </Select>
                    </Field>
                    {!browser && (
                      <Field label={mode === 'state' ? '状态 JSON（cookies / origins）' : '请求 JSON（method / url / headers / body）'}>
                        <input
                          aria-label="选择登录材料文件"
                          type="file"
                          accept=".json,application/json"
                          disabled={!!busy}
                          className="mb-2 block w-full rounded-lg border border-dashed border-hairline bg-surface-2/30 p-2.5 text-[12px] text-ink-2 file:mr-2 file:rounded file:border-0 file:bg-surface-2 file:px-2 file:py-1 file:text-[11px] file:text-ink-2"
                          onChange={e => readMaterial(e.target.files?.[0])}
                        />
                        <Textarea
                          required
                          disabled={reading || !!busy}
                          aria-label="登录材料 JSON"
                          rows={6}
                          className="font-mono text-[12px]"
                          value={material}
                          onChange={e => setMaterial(e.target.value)}
                          placeholder={mode === 'state' ? '{"cookies":[],"origins":[]}' : '{"method":"GET","url":"https://app.example/me","headers":{"Authorization":"Bearer …"}}'}
                        />
                        {reading && (
                          <p role="status" className="mt-2 text-[12px] text-ink-3">
                            正在读取文件…
                          </p>
                        )}
                      </Field>
                    )}
                  </div>
                </details>
                <Button type="submit" size="sm" disabled={!!busy || reading}>
                  {busy === 'create' ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}
                  {busy === 'create' ? '正在准备…' : browser ? '连接并打开' : '保存材料'}
                </Button>
              </form>
            </Panel>
          </div>
        )}

        <section aria-label="登录材料" className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="tw-section-title">浏览器与已保存材料</h2>
            <Badge size="sm" variant="muted">
              <span className="tw-nums font-mono">{entries.length}</span>
            </Badge>
            <span className="ml-auto text-[11px] leading-4 text-ink-3">
              多个浏览器可并行；相同站点不同账号需独立浏览器会话
            </span>
          </div>

          {list.loading ? (
            <SkeletonList count={2} />
          ) : entries.length === 0 ? (
            <Panel bodyClassName="p-0">
              <EmptyState
                icon={FileKey2}
                title="还没有登录现场"
                description="执行者申请时可以直接关联自己的浏览器，也可以手动连接。"
                action={
                  <Button size="sm" variant="outline" onClick={() => prepare()}>
                    <Plus className="h-3.5 w-3.5" /> 连接浏览器
                  </Button>
                }
              />
            </Panel>
          ) : (
            <div className="grid items-start gap-3 lg:grid-cols-2">
              {entries.map(entry => (
                <Panel
                  key={entry.id}
                  title={
                    <span className="flex min-w-0 flex-wrap items-center gap-2">
                      <Badge size="sm" variant="muted">{LABEL[entry.kind]}</Badge>
                      <span className="tw-break">{entry.name}</span>
                    </span>
                  }
                  extra={
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      className="text-ink-3 hover:bg-destructive/10 hover:text-destructive"
                      title={entry.lease ? '先归还现场再删除' : '删除材料'}
                      aria-label={`删除材料：${entry.name}`}
                      disabled={!!busy || !!entry.lease}
                      onClick={() => remove(entry)}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  }
                >
                  {(entry.url || entry.origin) && (
                    <p className="tw-break mb-3 font-mono text-[11px] leading-4 text-ink-3">
                      {entry.url || entry.origin}
                    </p>
                  )}
                  {entry.kind === 'browser' ? (
                    <>
                      <BrowserOccupancy
                        key={entry.lease?.id ?? 'idle'}
                        entry={entry}
                        busy={!!busy}
                        onChange={(op, note) => changeLease(entry, op, note)}
                      />
                      <div className="flex flex-wrap gap-1.5 border-t border-hairline pt-3">
                        <Button
                          size="sm"
                          variant="soft"
                          disabled={!!busy || (!!entry.lease && !canOperateBrowser(entry))}
                          onClick={() => action(entry, 'open')}
                        >
                          <Monitor className="h-3.5 w-3.5" /> 打开 / 接管
                        </Button>
                        <Button size="sm" variant="ghost" disabled={!!busy || !canOperateBrowser(entry)} onClick={() => action(entry, 'inspect')}>
                          查看页面
                        </Button>
                        <Button size="sm" variant="ghost" disabled={!!busy || !canOperateBrowser(entry)} onClick={() => action(entry, 'save')}>
                          保存状态
                        </Button>
                        <Button size="sm" variant="ghost" disabled={!!busy || !canOperateBrowser(entry)} onClick={() => action(entry, 'detach')}>
                          结束连接
                        </Button>
                      </div>
                    </>
                  ) : (
                    <p className="text-[12px] leading-relaxed text-ink-2">派发或续轮时提供给执行者。</p>
                  )}
                </Panel>
              ))}
            </div>
          )}

          {receipt && (
            <Panel
              title="当前浏览器页面"
              extra={<Badge size="sm" variant="info">已读取</Badge>}
            >
              <pre className="tw-inset tw-break max-h-48 overflow-auto whitespace-pre-wrap rounded-lg p-3 font-mono text-[11px] leading-relaxed text-ink-2">
                {receipt}
              </pre>
            </Panel>
          )}

          <p className="text-[11px] leading-relaxed text-ink-3">
            多站点可共用一个浏览器。相同站点的不同账号需独立浏览器会话；使用外部 CDP 时分别启动独立用户目录与端口。标签页共享登录状态，不能用于隔离同站点账号。
          </p>
        </section>
      </div>
    </Page>
  )
}

function OperatorCard({ item, entries, disabled, onPrepare, onComplete }: { item: OperatorRequest; entries: AccessEntry[]; disabled: boolean; onPrepare: () => void; onComplete: () => void }) {
  const [response, setResponse] = useState('')
  const [skipping, setSkipping] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const linked = entries.filter(entry => item.request.access_ids?.includes(entry.id))
  async function complete() {
    if (busy || disabled) return
    if (skipping && !response.trim()) { setError('请说明无法完成的原因或本次测试约束'); return }
    setBusy(true); setError('')
    try {
      const current = await api.access()
      for (const entry of current.filter(entry => item.request.access_ids?.includes(entry.id) && entry.kind === 'browser')) {
        if (canOperateBrowser(entry) && entry.lease) await api.browserLease(entry.id, { action: 'release', lease_id: entry.lease.id })
        else if (entry.lease) throw new Error('关联浏览器仍在使用或需要确认恢复，请先在下方处理')
      }
      await api.completeOperator(item.intent_id, item.request.id, response, skipping ? 'unavailable' : 'completed')
      onComplete()
    } catch (e) { setError(message(e)); onComplete() } finally { setBusy(false) }
  }
  return (
    <article className="tw-panel overflow-hidden rounded-xl border-l-2 border-l-warning">
      <header className="flex flex-wrap items-center gap-2 border-b border-hairline px-4 py-3">
        <h3 className="tw-break tw-section-title min-w-0">{item.title}</h3>
        <StatusIndicator compact status="pending" label="等待人工" className="ml-auto" />
      </header>
      <div className="space-y-3 p-4">
        <p className="whitespace-pre-wrap text-[13px] leading-relaxed text-ink-2">{item.request.request}</p>
        <div className="space-y-1">
          {linked.map(entry => (
            <p key={entry.id} className="tw-break font-mono text-[11px] leading-4 text-ink-3">
              {entry.name}{entry.url ? ` · ${entry.url}` : ''}
            </p>
          ))}
          {!linked.some(entry => entry.kind === 'browser') && (
            <Button size="sm" variant="outline" disabled={busy || disabled} onClick={onPrepare}>
              <Monitor className="h-3.5 w-3.5" /> 准备登录浏览器
            </Button>
          )}
        </div>
        {skipping && (
          <Callout tone="warning">
            无法完成时，执行者按现有访问能力继续，依赖登录的部分记录为未覆盖。
          </Callout>
        )}
        <Textarea
          aria-label={skipping ? '无法完成的原因与测试约束' : '人工完成说明'}
          disabled={busy || disabled}
          rows={2}
          maxLength={2000}
          placeholder={skipping ? '必填：无法完成的原因或仅测试未登录面的约束' : '可选：已扫码，可继续操作'}
          value={response}
          onChange={e => setResponse(e.target.value)}
          className="text-[12px]"
        />
        {error && <p className="text-[12px] leading-relaxed text-destructive">{error}</p>}
        <div className="flex flex-wrap gap-2">
          <Button size="sm" disabled={busy || disabled || (skipping && !response.trim())} onClick={complete}>
            {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : !skipping ? <Check className="h-3.5 w-3.5" /> : null}
            {busy ? '提交中…' : skipping ? '提交无法完成，继续评估' : '已完成，继续'}
            <ArrowRight className="h-3.5 w-3.5" />
          </Button>
          <Button size="sm" variant="outline" disabled={busy || disabled} onClick={() => { setSkipping(!skipping); setError('') }}>
            {skipping ? '返回' : '无法完成 / 跳过'}
          </Button>
        </div>
        <p className="text-[11px] leading-relaxed text-ink-3">提交时自动归还你领取的关联浏览器；执行者继续后核对登录状态。</p>
      </div>
    </article>
  )
}
