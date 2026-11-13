import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Layers,
  Power,
  RefreshCw,
  Search,
  ShieldCheck,
  Trash2,
  UserPlus,
  Users,
} from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Chip,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  EmptyState,
  Field,
  Input,
  ScrollArea,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SkeletonList,
  Spinner,
  StatusIndicator,
  Textarea,
  useConfirm,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { ApiError, api } from '@/lib/api'
import { fmtInt, fmtTime, oneLine, statusKind, statusLabel } from '@/lib/format'
import { useLoad, usePoll, type AsyncState } from '@/lib/hooks'
import type {
  ModelProfilesResponse,
  PeerRule,
  ProjectInfo,
  SpawnResponse,
  WorkerView,
  WorkersResponse,
} from '@/lib/types'

/** Raised statuses first: the roster is read top-down as "who is busy now". */
const STATUS_ORDER: Record<string, number> = {
  running: 0,
  claimed: 1,
  waiting: 2,
  pending: 3,
  parked: 4,
  failed: 5,
  canceled: 6,
  done: 7,
}

function workerRank(w: WorkerView): number {
  return STATUS_ORDER[w.status] ?? 8
}

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/** Structured codes the server may attach; shown as a mono badge next to the prose. */
function ErrorLine({ error }: { error: unknown }) {
  if (!error) return null
  const code = error instanceof ApiError ? error.code : undefined
  return (
    <div role="alert" className="flex flex-wrap items-center gap-2 text-[12px] text-destructive">
      <span>{errText(error)}</span>
      {code && (
        <Badge size="sm" variant="danger" className="font-mono">
          {code}
        </Badge>
      )}
    </div>
  )
}

/** Roster facet: one compact status filter above the list. */
type WorkerFacet = 'all' | 'running' | 'waiting' | 'failed' | 'done'

const FACETS: { id: WorkerFacet; label: string }[] = [
  { id: 'all', label: '全部' },
  { id: 'running', label: '在跑' },
  { id: 'waiting', label: '待办' },
  { id: 'failed', label: '失败' },
  { id: 'done', label: '终态' },
]

function facetOf(w: WorkerView): WorkerFacet {
  if (w.status === 'running' || w.status === 'claimed') return 'running'
  // A cancellation is not a failure: the row badge says 取消, and the roster
  // headline reads 停/终态. Bucket it with the terminal states instead of
  // 失败, and keep parked directions under 待办 (they can still be reopened).
  if (w.status === 'failed') return 'failed'
  if (w.status === 'done' || w.status === 'canceled') return 'done'
  return 'waiting'
}

/**
 * The collaboration page: spawn individuals (workers), watch the roster, kill
 * them, and decide who may talk to whom. In this ledger a worker *is* an intent
 * plus its latest run, so the page never invents a second identity.
 */
export function Workers({
  current,
  projects,
  onRefresh,
  onOpenGraphIntent,
  onOpenRun,
}: {
  current: string
  projects: ProjectInfo[]
  /** Reload the instance project list (the tier editor saves through it). */
  onRefresh: () => void
  /** Cross-page link (docs/34 §4): a worker's direction on the graph (`s:<id>`).
   *  Optional — without it no 图谱 entry is rendered. */
  onOpenGraphIntent?: (intentID: string) => void
  /** Cross-page link: this worker's latest run on the runs page. Optional. */
  onOpenRun?: (runID: string) => void
}) {
  const workers = usePoll(api.workers, 4000)
  const profiles = useLoad(api.modelProfiles, [])
  const project = projects.find((p) => p.id === current)
  const [spawnOpen, setSpawnOpen] = useState(false)
  const rows = workers.data?.workers ?? []
  const total = rows.length
  const running = rows.filter((w) => w.running).length

  return (
    <Page
      title="协作"
      subtitle="造人（worker）、看谁在跑、kill、模型档位与消息授权；一个 worker = 一个意图 + 一次运行"
      meta={
        <>
          <span className="tw-nums font-mono text-[11px] text-ink-3">工作者 {fmtInt(total)}</span>
          <span className="tw-nums font-mono text-[11px] text-ink-3">在跑 {fmtInt(running)}</span>
        </>
      }
      actions={
        <Button size="sm" onClick={() => setSpawnOpen(true)}>
          <UserPlus className="h-3.5 w-3.5" />
          造一个人
        </Button>
      }
      scroll={false}
    >
      <div className="grid h-full min-h-0 grid-cols-1 gap-4 overflow-y-auto p-4 sm:p-5 xl:grid-cols-[minmax(0,1fr)_360px] xl:overflow-hidden xl:p-6">
        <WorkerRoster
          workers={workers}
          onCreate={() => setSpawnOpen(true)}
          onOpenGraphIntent={onOpenGraphIntent}
          onOpenRun={onOpenRun}
        />
        <div className="flex min-h-0 flex-col gap-4 xl:overflow-y-auto xl:pr-1">
          <TierDefaultsPanel project={project} profiles={profiles} onSaved={onRefresh} />
          <PeerPolicyPanel />
          <p className="rounded-lg border border-hairline bg-surface-2/40 px-3 py-2 text-[11px] leading-4 text-ink-3">
            前提政策（硬前提门槛）已移至{' '}
            <a className="text-ink-2 underline underline-offset-2" href="/g/?view=settings">设置 → 项目设置</a>
            ，与项目默认模型和运行选项一起保存。
          </p>
        </div>
      </div>
      <SpawnDialog
        open={spawnOpen}
        onOpenChange={setSpawnOpen}
        project={project}
        profiles={profiles}
        onSpawned={() => workers.reload()}
      />
    </Page>
  )
}

function SpawnDialog({
  open,
  onOpenChange,
  project,
  profiles,
  onSpawned,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  project?: ProjectInfo
  profiles: AsyncState<ModelProfilesResponse>
  onSpawned: () => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[86vh] max-w-xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <UserPlus className="h-4 w-4 text-ink-3" />
            造一个人
          </DialogTitle>
          <DialogDescription>
            一个 worker = 一个意图 + 一次运行。容量满时返回 started=false，用同一个幂等键再点一次会启动它。
          </DialogDescription>
        </DialogHeader>
        {open && <SpawnForm project={project} profiles={profiles} onSpawned={onSpawned} />}
      </DialogContent>
    </Dialog>
  )
}

function WorkerRoster({
  workers,
  onCreate,
  onOpenGraphIntent,
  onOpenRun,
}: {
  workers: AsyncState<WorkersResponse>
  onCreate: () => void
  onOpenGraphIntent?: (intentID: string) => void
  onOpenRun?: (runID: string) => void
}) {
  const confirm = useConfirm()
  const [notice, setNotice] = useState('')
  const [fail, setFail] = useState<unknown>(null)
  const [query, setQuery] = useState('')
  const [facet, setFacet] = useState<WorkerFacet>('all')
  const [selected, setSelected] = useState<string | null>(null)
  const rows = [...(workers.data?.workers ?? [])].sort(
    (a, b) =>
      workerRank(a) - workerRank(b) ||
      (b.created_at > a.created_at ? 1 : b.created_at < a.created_at ? -1 : 0),
  )
  const running = rows.filter((w) => w.running).length
  const needle = query.trim().toLowerCase()
  const filtered = rows.filter((w) => {
    if (facet !== 'all' && facetOf(w) !== facet) return false
    if (!needle) return true
    return [w.title, w.intent_id, w.participant_id, w.profile, w.resolved_profile, w.model, w.run_id, w.node]
      .filter(Boolean)
      .join(' ')
      .toLowerCase()
      .includes(needle)
  })

  async function kill(w: WorkerView) {
    const ok = await confirm({
      title: 'kill 这个工作者？',
      description: `${w.title}\n${w.intent_id}\n\n它会先被置 Hold（调度不会复活），然后取消当前 run；已结束的 worker 不能 kill。`,
      confirmLabel: 'kill',
      destructive: true,
    })
    if (!ok) return
    setFail(null)
    setNotice('')
    try {
      const res = await api.killWorker(w.intent_id)
      setNotice(
        `已 kill ${w.intent_id}${res.canceled ? '（run 已取消）' : '（无在跑 run，已落终态）'}`,
      )
      workers.reload()
    } catch (e) {
      setFail(e)
    }
  }

  const firstLoad = workers.loading && !workers.data

  return (
    <Panel
      title={`工作者（${fmtInt(rows.length)}）`}
      className="flex min-h-0 flex-col"
      bodyClassName="flex min-h-0 flex-1 flex-col p-0"
      extra={
        <>
          <span className="tw-nums font-mono text-[11px] text-ink-3">在跑 {running}</span>
        </>
      }
    >
      {/* One 12px toolbar: search + status facets + refresh, all h-8. */}
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline px-3 py-2">
        <div className="relative min-w-0 flex-1 basis-[160px]">
          <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="搜索标题 / 意图 / 执行者 / 模型"
            className="h-8 pl-7 text-[12px]"
            aria-label="过滤工作者"
          />
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          {FACETS.map((f) => (
            <Chip
              key={f.id}
              active={facet === f.id}
              onClick={() => setFacet(f.id)}
              className="h-8 rounded-md px-2.5 text-[12px]"
            >
              {f.label}
            </Chip>
          ))}
        </div>
        <Button
          size="icon-sm"
          variant="outline"
          className="shrink-0"
          aria-label="刷新工作者名单"
          title="立即刷新（页面每 4 秒也会自动刷新）"
          onClick={() => workers.reload()}
        >
          <RefreshCw className={`h-3.5 w-3.5 ${workers.loading ? 'animate-spin' : ''}`} />
        </Button>
      </div>

      {(workers.error || fail || notice) && (
        <div className="shrink-0 space-y-2 border-b border-hairline p-3">
          {workers.error && (
            <Callout tone="destructive">
              <div className="flex flex-wrap items-center gap-2">
                <span>读工作者列表失败：{workers.error}</span>
                <Button size="xs" variant="outline" onClick={() => workers.reload()}>重试</Button>
              </div>
            </Callout>
          )}
          {notice && <Callout tone="success">{notice}</Callout>}
          <ErrorLine error={fail} />
        </div>
      )}

      <ScrollArea className="min-h-0 flex-1">
        <div className="p-2">
          {firstLoad && <SkeletonList count={4} />}
          {!firstLoad && rows.length === 0 && (
            <EmptyState
              compact
              icon={Users}
              title="还没有工作者"
              description="造第一个人，或让执行者在运行中造它的后代；名单每 4 秒自动刷新。"
              action={<Button size="sm" onClick={onCreate}><UserPlus className="h-3.5 w-3.5" />造一个人</Button>}
            />
          )}
          {!firstLoad && rows.length > 0 && filtered.length === 0 && (
            <EmptyState
              compact
              icon={Users}
              title="没有匹配的工作者"
              description={`当前过滤：${facet === 'all' ? '全部状态' : FACETS.find((f) => f.id === facet)?.label}${needle ? ` · ${query.trim()}` : ''}`}
              action={
                <Button size="sm" variant="outline" onClick={() => { setQuery(''); setFacet('all') }}>
                  清除筛选
                </Button>
              }
            />
          )}
          <div className="divide-y divide-hairline">
            {filtered.map((w) => (
              <WorkerRow
                key={w.intent_id}
                w={w}
                selected={selected === w.intent_id}
                onSelect={() => setSelected(selected === w.intent_id ? null : w.intent_id)}
                onKill={() => kill(w)}
                onOpenGraphIntent={onOpenGraphIntent}
                onOpenRun={onOpenRun}
              />
            ))}
          </div>
          {rows.length > 0 && (
            <div className="px-1.5 py-2 font-mono text-[11px] text-ink-3">
              排序：在跑 → 待办/等待 → 停/终态；名单每 4 秒刷新
            </div>
          )}
        </div>
      </ScrollArea>
    </Panel>
  )
}

function WorkerRow({
  w,
  selected,
  onSelect,
  onKill,
  onOpenGraphIntent,
  onOpenRun,
}: {
  w: WorkerView
  selected: boolean
  onSelect: () => void
  onKill: () => void
  onOpenGraphIntent?: (intentID: string) => void
  onOpenRun?: (runID: string) => void
}) {
  const terminal = !w.can_kill
  // A 2px rail carries the semantic colour in a small area; running is the only
  // "live" tone, failed is the only alarm tone.
  const rail = w.running
    ? 'border-l-info/60'
    : w.status === 'failed'
      ? 'border-l-destructive/60'
      : 'border-l-transparent'
  return (
    <article
      data-worker-id={w.intent_id}
      data-selected={selected ? 'true' : undefined}
      className={`border-l-2 transition-colors ${rail} ${selected ? 'bg-surface-3/70' : 'hover:bg-surface-2/60'}`}
    >
      <div className="flex items-stretch gap-2 px-2.5 py-2">
        <button
          type="button"
          onClick={onSelect}
          aria-expanded={selected}
          className="flex min-w-0 flex-1 flex-col justify-center gap-1 rounded-md px-1 py-0.5 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          <span className="flex min-w-0 items-center gap-2">
            <StatusIndicator compact status={statusKind(w.status)} label={statusLabel(w.status)} />
            {w.running && <StatusIndicator compact status="running" label="生成中" />}
            {w.hold && <Badge size="sm" variant="outline">Hold</Badge>}
            {w.parent_intent_id && (
              <Badge size="sm" variant="secondary" title={`父意图 ${w.parent_intent_id}`}>
                子任务
              </Badge>
            )}
            <span className="min-w-0 truncate text-[13px] font-medium text-ink">{w.title}</span>
          </span>
          <span className="flex min-w-0 items-center gap-2 font-mono text-[11px] text-ink-3">
            <span className="min-w-0 truncate" title={w.intent_id}>{w.intent_id}</span>
            {w.run_id && (
              <>
                <span aria-hidden className="text-ink-3/60">·</span>
                <span className="min-w-0 truncate" title={w.run_id}>run {w.run_id}</span>
                {w.run_status ? <span className="shrink-0">（{statusLabel(w.run_status)}）</span> : null}
              </>
            )}
            <span className="ml-auto shrink-0">{fmtTime(w.created_at)}</span>
          </span>
        </button>
        <div className="flex shrink-0 items-center gap-1 self-center">
          {/* 跨页联动（docs/34 §4）：worker = 意图 + 最近 run，所以两个入口
              各自指向图谱的 s:<id> 与运行页的 run。回调没传时不渲染。 */}
          {onOpenGraphIntent && w.intent_id && (
            <Button
              size="xs"
              variant="ghost"
              data-testid="worker-open-graph"
              title="在图谱中选中这个方向"
              onClick={() => onOpenGraphIntent(w.intent_id)}
            >
              图谱
            </Button>
          )}
          {onOpenRun && w.run_id && (
            <Button
              size="xs"
              variant="ghost"
              data-testid="worker-open-run"
              title="在运行页打开这个 worker 的最近 run"
              onClick={() => onOpenRun(w.run_id!)}
            >
              运行
            </Button>
          )}
          <Button
            size="xs"
            variant="outline"
            className={terminal ? 'shrink-0' : 'shrink-0 border-destructive/30 text-destructive hover:border-destructive/50 hover:bg-destructive/10 hover:text-destructive'}
            disabled={terminal}
            title={terminal ? '已结束：不用再 kill' : '置 Hold + 取消当前 run（需二次确认）'}
            onClick={onKill}
          >
            <Power className="h-3 w-3" />
            kill
          </Button>
        </div>
      </div>

      {selected && (
        <div className="space-y-1.5 border-t border-hairline px-3 py-2">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px] text-ink-3">
            <span>参与者 {w.participant_id}</span>
            {w.profile && <span>profile {w.profile}</span>}
            {w.resolved_profile && w.resolved_profile !== w.profile && (
              <span title="run 实际解析到的模型配置">实际 {w.resolved_profile}</span>
            )}
            {w.tier && <Badge size="sm" variant="secondary">{w.tier}</Badge>}
            {w.model && <span>model {w.model}</span>}
            {w.node && <span>node {w.node}</span>}
          </div>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px] text-ink-3">
            {w.parent_intent_id && <span>父 {w.parent_intent_id}</span>}
            {w.delegated_by_run_id && <span>委派 run {w.delegated_by_run_id}</span>}
            {w.created_by_participant_id && <span>创建者 {w.created_by_participant_id}</span>}
            {w.last_event_kind && <span className="uppercase tracking-wide">{w.last_event_kind}</span>}
            {w.last_event_at && <span className="ml-auto">{fmtTime(w.last_event_at)}</span>}
          </div>
          <div className="flex items-start gap-2 text-[11px] leading-relaxed text-ink-2">
            <span className="shrink-0 text-ink-3">最近事件</span>
            <span className="tw-break min-w-0 flex-1">{oneLine(w.last_event_text ?? '', 220) || '—'}</span>
          </div>
        </div>
      )}
    </article>
  )
}

/** Cryptographically random when available; readable and unique enough otherwise. */
function newIdempotencyKey(): string {
  const rand =
    typeof crypto !== 'undefined' && 'randomUUID' in crypto
      ? crypto.randomUUID()
      : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
  return `ui-spawn-${rand}`
}

function SpawnForm({
  project,
  profiles,
  onSpawned,
}: {
  project?: ProjectInfo
  profiles: AsyncState<ModelProfilesResponse>
  onSpawned: () => void
}) {
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [mode, setMode] = useState<'default' | 'profile' | 'tier'>('default')
  const [profile, setProfile] = useState('')
  const [tier, setTier] = useState('worker')
  const [key, setKey] = useState(() => newIdempotencyKey())
  const [busy, setBusy] = useState(false)
  const [fail, setFail] = useState<unknown>(null)
  const [outcome, setOutcome] = useState<SpawnResponse | null>(null)
  const titleRef = useRef<HTMLInputElement | null>(null)

  const all = profiles.data?.profiles ?? []
  const active = profiles.data?.active ?? ''

  async function submit() {
    if (!title.trim() && !description.trim()) {
      setFail(new Error('标题或任务书至少填一个'))
      return
    }
    // "指定 profile" with nothing selected would silently fall back to the
    // project default, i.e. start a different model than the operator picked.
    if (mode === 'profile' && !profile) {
      setFail(new Error('已选择「指定 profile」，但还没有选具体配置：请先选一个，或改回默认'))
      return
    }
    setBusy(true)
    setFail(null)
    setOutcome(null)
    try {
      const res = await api.spawnWorker({
        title: title.trim(),
        description: description.trim(),
        idempotency_key: key,
        ...(mode === 'profile' && profile ? { profile } : {}),
        ...(mode === 'tier' && tier ? { tier } : {}),
      })
      setOutcome(res)
      onSpawned()
      if (res.started) {
        // A new individual is running; the next spawn is a different person, so
        // give it a fresh idempotency key.
        setTitle('')
        setDescription('')
        setKey(newIdempotencyKey())
      }
      // started=false keeps the key on purpose: retrying it starts this same
      // worker instead of creating a duplicate.
    } catch (e) {
      setFail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      <Field label="标题" hint="一句话：这个人负责回答什么">
        <Input
          ref={titleRef}
          autoFocus
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="例如：核对 /admin 的越权读取面"
          className="text-[13px]"
        />
      </Field>
      <Field label="任务书" hint="写清目标、授权范围、验收标准，以及需要引用的事实或证据">
        <Textarea
          rows={3}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={'普通用户能否读取他人订单？只做只读验证，不改数据；从事实 f_xxx 开始。'}
          className="text-[12px] leading-relaxed"
        />
      </Field>
      <Field
        label="模型绑定"
        hint={`项目档位：worker=${project?.worker_profile || '未配置'}，strong=${project?.strong_profile || '未配置'}；未配置的档位会由服务端报错`}
      >
        <Select value={mode} onValueChange={(v) => setMode(v as typeof mode)}>
          <SelectTrigger className="h-9 text-[12px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="default" className="text-[12px]">
              默认（项目 worker 档 → 实例 active{active ? `：${active}` : ''}）
            </SelectItem>
            <SelectItem value="profile" className="text-[12px]">
              指定 profile
            </SelectItem>
            <SelectItem value="tier" className="text-[12px]">
              指定档位（tier）
            </SelectItem>
          </SelectContent>
        </Select>
      </Field>
      {mode === 'profile' && (
        <Field
          label="profile"
          hint="显示 tier / 容量(running/max_concurrency) / 端点可用性；不可执行或端点不可用的配置禁用"
        >
          <Select value={profile || undefined} onValueChange={setProfile}>
            <SelectTrigger className="h-9 text-[12px]">
              <SelectValue placeholder={all.length ? '选择模型配置' : '没有可用配置'} />
            </SelectTrigger>
            <SelectContent>
              {all.map((p) => {
                const unavailable = !p.runnable || (p.probed && !p.available)
                const cap = `${p.running ?? 0}/${p.max_concurrency ? p.max_concurrency : '∞'}`
                const why = !p.runnable ? '不可执行' : p.probed && !p.available ? '端点不可用' : ''
                return (
                  <SelectItem
                    key={p.id}
                    value={p.id}
                    disabled={unavailable}
                    className="text-[12px]"
                    title={unavailable ? `${why}${p.probe_error ? `：${p.probe_error}` : ''}` : cap}
                  >
                    <span className="font-mono">{p.id}</span>
                    {p.tier ? (
                      <span className="ml-2 text-[11px] text-ink-3">[{p.tier}]</span>
                    ) : null}
                    <span className="tw-nums ml-2 font-mono text-[11px] text-ink-3">容量 {cap}</span>
                    {p.model ? <span className="ml-2 text-[11px] text-ink-3">{p.model}</span> : null}
                    {why ? <span className="ml-2 text-[11px] text-destructive">{why}</span> : null}
                  </SelectItem>
                )
              })}
            </SelectContent>
          </Select>
        </Field>
      )}
      {mode === 'tier' && (
        <Field label="资源路由（可选）" hint="按项目 worker_profile/strong_profile 选择配置；不决定职责或模型推理强度">
          <Select value={tier} onValueChange={setTier}>
            <SelectTrigger className="h-9 text-[12px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="worker" className="text-[12px]">
                worker（项目执行者默认配置）
              </SelectItem>
              <SelectItem value="strong" className="text-[12px]">
                strong（项目指挥官配置）
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
      )}
      <Field
        label="幂等键（自动生成）"
        hint="重试用同一个键会找回同一个 worker，不会重复造人；容量满时保留此键再点一次即可启动它"
      >
        <div className="flex items-center gap-2">
          <Input
            value={key}
            readOnly
            className="h-8 font-mono text-[11px]"
            aria-label="幂等键"
          />
          <Button
            size="sm"
            variant="outline"
            className="shrink-0"
            onClick={() => { setKey(newIdempotencyKey()); titleRef.current?.focus() }}
            title="换一个键：这会变成一个新的人"
          >
            重新生成
          </Button>
        </div>
      </Field>

      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" onClick={() => void submit()} disabled={busy}>
          {busy ? <Spinner size="sm" className="text-primary-foreground" /> : <UserPlus className="h-3.5 w-3.5" />}
          {busy ? '造人中…' : '造一个人'}
        </Button>
        <span className="text-[11px] leading-4 text-ink-3">
          容量满时返回 started=false + reason，不会重复创建
        </span>
      </div>

      <ErrorLine error={fail} />
      {outcome && (
        <Callout tone={outcome.started ? 'success' : 'warning'}>
          <div className="space-y-1">
            <div>
              {outcome.created ? '已创建' : '幂等命中（未新建）'}：{' '}
              <span className="font-mono">{outcome.intent_id}</span>
              {' · '}started={String(outcome.started)}
            </div>
            {outcome.run_id && (
              <div>
                run <span className="font-mono">{outcome.run_id}</span>
                {outcome.resolved_model ? ` · ${outcome.resolved_model}` : ''}
              </div>
            )}
            {!outcome.started && outcome.reason && (
              <div>
                原因：{outcome.reason}
                <div className="mt-0.5 opacity-80">用同一个幂等键再点一次「造一个人」会启动它，而不是创建新 worker。</div>
              </div>
            )}
            {outcome.worker?.profile && (
              <div className="font-mono text-[11px] opacity-80">
                profile {outcome.worker.profile}
                {outcome.worker.tier ? ` · tier ${outcome.worker.tier}` : ''}
              </div>
            )}
          </div>
        </Callout>
      )}
    </div>
  )
}

/** Radix Select forbids an empty item value, so "clear" needs a sentinel. */
const INHERIT = '__inherit'

/**
 * Project-level model defaults. worker_profile is the default every intent
 * that names no profile runs on; strong_profile selects the commander and can
 * also be requested by workers. Existing runs keep their frozen binding;
 * clearing a tier makes subsequent runs inherit the instance active profile.
 */
function TierDefaultsPanel({
  project,
  profiles,
  onSaved,
}: {
  project?: ProjectInfo
  profiles: AsyncState<ModelProfilesResponse>
  onSaved: () => void
}) {
  const [worker, setWorker] = useState('')
  const [strong, setStrong] = useState('')
  const [busy, setBusy] = useState(false)
  const [fail, setFail] = useState<unknown>(null)
  const [notice, setNotice] = useState('')
  const projectId = project?.id ?? ''

  // The server (project list) is the source of truth; follow it after a save.
  useEffect(() => {
    setWorker(project?.worker_profile ?? '')
    setStrong(project?.strong_profile ?? '')
  }, [projectId, project?.worker_profile, project?.strong_profile])

  const list = profiles.data?.profiles ?? []
  const active = profiles.data?.active ?? ''
  // A saved id can outlive its catalogue entry (profile deleted): keep it visible
  // so the select does not silently look like "inherit".
  const extra = [worker, strong].filter((id) => id && !list.some((p) => p.id === id))
  const options = [
    ...list.map((p) => ({
      id: p.id,
      label: [p.id, p.tier ? `[${p.tier}]` : '', p.model ?? ''].filter(Boolean).join(' '),
      note: [
        p.tier ? `${p.tier} 档` : '',
        `容量 ${p.running ?? 0}/${p.max_concurrency ? p.max_concurrency : '∞'}`,
        !p.runnable ? '不可执行（字段不全）' : p.probed && !p.available ? '端点不可用' : '',
      ]
        .filter(Boolean)
        .join(' · '),
      warn: !p.runnable || (p.probed && !p.available),
      disabled: !p.runnable || (p.probed && !p.available),
      title: p.probe_error ?? '',
    })),
    ...extra.map((id) => ({
      id,
      label: id,
      note: list.length ? '不在当前模型配置列表' : '',
      warn: true,
      disabled: false,
      title: '',
    })),
  ]

  async function save() {
    if (!projectId) return
    setBusy(true)
    setFail(null)
    setNotice('')
    try {
      const saved = await api.updateProject(projectId, {
        // "" clears the project default: dispatch then inherits the instance
        // active profile.
        worker_profile: worker,
        strong_profile: strong,
      })
      setWorker(saved.worker_profile ?? '')
      setStrong(saved.strong_profile ?? '')
      setNotice(
        `已保存：worker=${saved.worker_profile || '继承实例 active'}`
        + `，strong=${saved.strong_profile || '继承实例 active'}`,
      )
      onSaved()
    } catch (e) {
      setFail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Panel title="默认模型" extra={<Layers className="h-3.5 w-3.5 text-ink-3" />}>
      <div className="space-y-3">
        <Callout tone="info">
          <div className="space-y-1">
            <div>
              <span className="font-mono">worker_profile</span>＝执行者默认模型：没有指定 profile 的意图/子意图都用它；
              <span className="font-mono">strong_profile</span>＝指挥官模型。两者可选同一配置，职责与模型档位独立。
            </div>
            <div>
              指挥官使用项目选定模型；留空时继承实例默认配置（{active || '未配置'}）。
              正在运行的轮次保留原模型，后续轮次使用新配置。
            </div>
          </div>
        </Callout>

        {profiles.error && (
          <Callout tone="destructive">
            <div className="flex flex-wrap items-center gap-2">
              <span>读模型配置失败：{profiles.error}</span>
              <Button size="xs" variant="outline" onClick={() => void profiles.reload?.()}>重试</Button>
            </div>
          </Callout>
        )}

        <Field
          label="执行者默认模型"
          hint={
            project
              ? `当前：${project.worker_profile || '继承实例 active'}`
              : '项目信息加载中…'
          }
        >
          <Select
            value={worker || INHERIT}
            onValueChange={(v) => setWorker(v === INHERIT ? '' : v)}
            disabled={!projectId}
          >
            <SelectTrigger className="h-9 text-[12px]">
              <SelectValue placeholder="继承实例 active" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={INHERIT} className="text-[12px]">
                继承实例 active（清空）{active ? `：${active}` : ''}
              </SelectItem>
              {options.map((p) => (
                <SelectItem
                  key={p.id}
                  value={p.id}
                  disabled={p.disabled}
                  className="text-[12px]"
                  title={p.title || undefined}
                >
                  <span className="font-mono">{p.label}</span>
                  {p.note ? (
                    <span className={`ml-2 text-[11px] ${p.warn ? 'text-warning' : 'text-ink-3'}`}>
                      {p.note}
                    </span>
                  ) : null}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <Field
          label="指挥官模型"
          hint={
            project
              ? `当前：${project.strong_profile || '继承实例 active'}；用于后续指挥官轮次`
              : '项目信息加载中…'
          }
        >
          <Select
            value={strong || INHERIT}
            onValueChange={(v) => setStrong(v === INHERIT ? '' : v)}
            disabled={!projectId}
          >
            <SelectTrigger className="h-9 text-[12px]">
              <SelectValue placeholder="继承实例 active" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={INHERIT} className="text-[12px]">
                继承实例 active（清空）{active ? `：${active}` : ''}
              </SelectItem>
              {options.map((p) => (
                <SelectItem
                  key={p.id}
                  value={p.id}
                  disabled={p.disabled}
                  className="text-[12px]"
                  title={p.title || undefined}
                >
                  <span className="font-mono">{p.label}</span>
                  {p.note ? (
                    <span className={`ml-2 text-[11px] ${p.warn ? 'text-warning' : 'text-ink-3'}`}>
                      {p.note}
                    </span>
                  ) : null}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="secondary" onClick={save} disabled={busy || !projectId}>
            {busy && <Spinner size="sm" className="text-ink-2" />}
            {busy ? '保存中…' : '保存默认模型'}
          </Button>
          <span className="text-[11px] leading-4 text-ink-3">
            保存只影响下一轮派发；已在跑的 run 不换模型
          </span>
        </div>
        {notice && <div className="text-[11px] text-success">{notice}</div>}
        <ErrorLine error={fail} />
      </div>
    </Panel>
  )
}

function PeerPolicyPanel() {
  const peers = useLoad(api.peers, [])
  const people = usePoll(api.participants, 8000)
  const [mode, setMode] = useState<'open' | 'allowlist'>('open')
  const [rows, setRows] = useState<PeerRule[]>([])
  const [busy, setBusy] = useState(false)
  const [fail, setFail] = useState<unknown>(null)
  const [notice, setNotice] = useState('')

  // The server is the source of truth; reset the editor whenever it answers.
  useEffect(() => {
    if (!peers.data) return
    setMode(peers.data.mode === 'allowlist' ? 'allowlist' : 'open')
    setRows(peers.data.peer_policy.allow ?? [])
  }, [peers.data])

  const participantRows = people.data ?? []
  const options = useMemo(() => {
    const seen = new Set(participantRows.map((p) => p.id))
    const extras = rows
      .flatMap((r) => [r.from, r.to])
      .filter((v) => v && !seen.has(v))
      .map((id) => ({ id, title: id }))
    return [...participantRows.map((p) => ({ id: p.id, title: p.title })), ...extras]
  }, [participantRows, rows])

  function addRow() {
    setRows((cur) => [...cur, { from: '', to: '' }])
  }

  function setRow(i: number, patch: Partial<PeerRule>) {
    setRows((cur) => cur.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))
  }

  function removeRow(i: number) {
    setRows((cur) => cur.filter((_, idx) => idx !== i))
  }

  async function save() {
    if (mode === 'allowlist') {
      const incomplete = rows.some((r) => !r.from || !r.to)
      if (incomplete) {
        setFail(new Error('每条规则都要选 from 与 to；不完整的行请删除'))
        return
      }
      const self = rows.find((r) => r.from === r.to)
      if (self) {
        setFail(new Error('from 与 to 不能是同一个人（自己发给自己在服务端也会被拒）'))
        return
      }
    }
    setBusy(true)
    setFail(null)
    setNotice('')
    try {
      const clean = rows.filter((r) => r.from && r.to)
      const res = await api.savePeers({ mode, allow: mode === 'allowlist' ? clean : [] })
      peers.setData(res)
      setNotice(`已保存：mode=${res.mode}${res.mode === 'allowlist' ? `，${clean.length} 条有向规则` : ''}`)
    } catch (e) {
      setFail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Panel
      title="消息授权（peer 策略）"
      extra={<ShieldCheck className="h-3.5 w-3.5 text-ink-3" />}
    >
      <div className="space-y-3">
        <Field
          label="模式"
          hint="open：同项目任意参与者可互发；allowlist：只允许下面列出的有向 from→to（commander 与发给自己除外）"
        >
          <Select value={mode} onValueChange={(v) => setMode(v as typeof mode)}>
            <SelectTrigger className="h-9 text-[12px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="open" className="text-[12px]">
                open（默认，全项目可互发）
              </SelectItem>
              <SelectItem value="allowlist" className="text-[12px]">
                allowlist（只允许显式授权）
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        {peers.error && (
          <Callout tone="destructive">
            <div className="flex flex-wrap items-center gap-2">
              <span>读 peer 策略失败：{peers.error}</span>
              <Button size="xs" variant="outline" onClick={() => peers.reload()}>重试</Button>
            </div>
          </Callout>
        )}

        {mode === 'allowlist' && (
          <div className="space-y-1.5">
            {rows.length === 0 && (
              <div className="tw-inset rounded-md px-2.5 py-2 text-[12px] text-ink-2">
                allowlist 为空 = 除 commander 通道外都不能互发。用「加一条」授权方向。
              </div>
            )}
            {rows.map((r, i) => (
              <div key={i} className="flex items-center gap-1.5">
                <Select
                  value={r.from || undefined}
                  onValueChange={(v) => setRow(i, { from: v })}
                >
                  <SelectTrigger className="h-8 min-w-0 flex-1 text-[12px]">
                    <SelectValue placeholder="from" />
                  </SelectTrigger>
                  <SelectContent>
                    {options.map((p) => (
                      <SelectItem key={p.id} value={p.id} className="text-[12px]">
                        <span className="font-mono">{p.id}</span>
                        <span className="ml-2 text-[11px] text-ink-3">{p.title}</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <span className="shrink-0 text-[11px] text-ink-3">→</span>
                <Select value={r.to || undefined} onValueChange={(v) => setRow(i, { to: v })}>
                  <SelectTrigger className="h-8 min-w-0 flex-1 text-[12px]">
                    <SelectValue placeholder="to" />
                  </SelectTrigger>
                  <SelectContent>
                    {options.map((p) => (
                      <SelectItem key={p.id} value={p.id} className="text-[12px]">
                        <span className="font-mono">{p.id}</span>
                        <span className="ml-2 text-[11px] text-ink-3">{p.title}</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  className="shrink-0"
                  onClick={() => removeRow(i)}
                  title="删除这条方向"
                  aria-label="删除这条方向"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
            <div className="flex flex-wrap items-center gap-2 pt-0.5">
              <Button size="sm" variant="outline" onClick={addRow} disabled={options.length === 0}>
                加一条
              </Button>
              {options.length === 0 && (
                <span className="text-[11px] text-ink-3">
                  还没有参与者：先造一个人，再设置 who→who。
                </span>
              )}
            </div>
          </div>
        )}

        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="secondary" onClick={save} disabled={busy || !peers.data}>
            {busy && <Spinner size="sm" className="text-ink-2" />}
            {busy ? '保存中…' : '保存策略'}
          </Button>
          {notice && <span className="text-[11px] text-success">{notice}</span>}
        </div>
        <ErrorLine error={fail} />
      </div>
    </Panel>
  )
}
