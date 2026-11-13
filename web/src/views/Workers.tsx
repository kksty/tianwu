import { useEffect, useMemo, useState } from 'react'
import {
  Layers,
  Power,
  RefreshCw,
  ShieldCheck,
  Trash2,
  UserPlus,
  Users,
} from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
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
  StatusIndicator,
  Textarea,
  useConfirm,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { ApiError, api } from '@/lib/api'
import { fmtInt, fmtTime, oneLine, premisePolicyHint, premisePolicyLabel, statusKind, statusLabel } from '@/lib/format'
import { useLoad, usePoll, type AsyncState } from '@/lib/hooks'
import type {
  ModelProfilesResponse,
  PeerRule,
  PremisePolicy,
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
    <div className="flex flex-wrap items-center gap-2 text-[12px] text-destructive">
      <span>{errText(error)}</span>
      {code && (
        <Badge size="sm" variant="danger" className="font-mono">
          {code}
        </Badge>
      )}
    </div>
  )
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

  return (
    <Page
      title="协作"
      subtitle="造人（worker）、看谁在跑、kill、模型档位与消息授权；一个 worker = 一个意图 + 一次运行"
      scroll={false}
    >
      <div className="grid h-full min-h-0 grid-cols-1 gap-3 py-3 xl:grid-cols-[1fr_400px]">
        <WorkerRoster
          workers={workers}
          onOpenGraphIntent={onOpenGraphIntent}
          onOpenRun={onOpenRun}
        />
        <div className="flex min-h-0 flex-col gap-3 overflow-y-auto pr-0.5">
          <SpawnForm
            project={project}
            profiles={profiles}
            onSpawned={() => workers.reload()}
          />
          <TierDefaultsPanel project={project} profiles={profiles} onSaved={onRefresh} />
          <PeerPolicyPanel />
          <PremisePolicyPanel />
        </div>
      </div>
    </Page>
  )
}

function WorkerRoster({
  workers,
  onOpenGraphIntent,
  onOpenRun,
}: {
  workers: AsyncState<WorkersResponse>
  onOpenGraphIntent?: (intentID: string) => void
  onOpenRun?: (runID: string) => void
}) {
  const confirm = useConfirm()
  const [notice, setNotice] = useState('')
  const [fail, setFail] = useState<unknown>(null)
  const rows = [...(workers.data?.workers ?? [])].sort(
    (a, b) =>
      workerRank(a) - workerRank(b) ||
      (b.created_at > a.created_at ? 1 : b.created_at < a.created_at ? -1 : 0),
  )
  const running = rows.filter((w) => w.running).length

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

  return (
    <Panel
      title={`工作者（${fmtInt(rows.length)}）`}
      className="flex min-h-0 flex-col"
      bodyClassName="min-h-0 flex-1 overflow-hidden p-0"
      extra={
        <>
          <span className="font-mono text-[11px] text-muted-foreground">在跑 {running}</span>
          <Button
            size="xs"
            variant="outline"
            onClick={() => workers.reload()}
            title="立即刷新（页面每 4 秒也会自动刷新）"
          >
            <RefreshCw className="h-3 w-3" />
            刷新
          </Button>
        </>
      }
    >
      <div className="space-y-2 border-b border-border/60 p-3">
        {workers.error && <Callout tone="destructive">读工作者列表失败：{workers.error}</Callout>}
        {notice && <Callout tone="success">{notice}</Callout>}
        <ErrorLine error={fail} />
      </div>
      <ScrollArea className="h-full">
        <div className="space-y-1.5 p-3">
          {workers.loading && !workers.data && <SkeletonList count={3} />}
          {!workers.loading && rows.length === 0 && (
            <EmptyState
              compact
              icon={Users}
              title="还没有工作者"
              description="用右侧「造一个人」创建第一个；执行者也会在运行中造自己的后代。"
            />
          )}
          {rows.map((w) => (
            <WorkerRow
              key={w.intent_id}
              w={w}
              onKill={() => kill(w)}
              onOpenGraphIntent={onOpenGraphIntent}
              onOpenRun={onOpenRun}
            />
          ))}
          {rows.length > 0 && (
            <div className="pt-1 font-mono text-[10.5px] text-muted-foreground">
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
  onKill,
  onOpenGraphIntent,
  onOpenRun,
}: {
  w: WorkerView
  onKill: () => void
  onOpenGraphIntent?: (intentID: string) => void
  onOpenRun?: (runID: string) => void
}) {
  const terminal = !w.can_kill
  return (
    <div className="rounded-md border border-border bg-card px-3 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <StatusIndicator status={statusKind(w.status)} label={statusLabel(w.status)} />
        {w.running && <StatusIndicator status="running" label="在跑" compact />}
        {w.parent_intent_id && (
          <Badge size="sm" variant="secondary" title={`父意图 ${w.parent_intent_id}`}>
            子任务
          </Badge>
        )}
        {w.hold && (
          <Badge size="sm" variant="outline">
            Hold
          </Badge>
        )}
        <span className="min-w-0 truncate text-[13px] font-medium text-foreground">{w.title}</span>
        <span className="font-mono text-[10.5px] text-muted-foreground">{w.intent_id}</span>
        <div className="ml-auto flex items-center gap-1.5">
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
            variant={terminal ? 'outline' : 'destructive'}
            disabled={terminal}
            title={terminal ? '已结束：不用再 kill' : '置 Hold + 取消当前 run（需二次确认）'}
            onClick={onKill}
          >
            <Power className="h-3 w-3" />
            kill
          </Button>
        </div>
      </div>

      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[10.5px] text-muted-foreground">
        <span>参与者 {w.participant_id}</span>
        {w.profile && <span>profile {w.profile}</span>}
        {w.resolved_profile && w.resolved_profile !== w.profile && (
          <span title="run 实际解析到的模型配置">实际 {w.resolved_profile}</span>
        )}
        {w.tier && <Badge size="sm" variant="secondary">{w.tier}</Badge>}
        {w.model && <span>model {w.model}</span>}
        {w.node && <span>node {w.node}</span>}
        {w.run_id && (
          <span>
            run {w.run_id}
            {w.run_status ? `（${statusLabel(w.run_status)}）` : ''}
          </span>
        )}
        {w.parent_intent_id && <span>父 {w.parent_intent_id}</span>}
        {w.delegated_by_run_id && <span>委派 run {w.delegated_by_run_id}</span>}
        {w.created_by_participant_id && <span>创建者 {w.created_by_participant_id}</span>}
        <span className="ml-auto">{fmtTime(w.created_at)}</span>
      </div>

      <div className="mt-1 flex items-start gap-1.5 text-[11px] text-muted-foreground">
        <span className="shrink-0 font-mono text-[10px] uppercase tracking-wider">
          {w.last_event_kind || '最近事件'}
        </span>
        <span className="tw-break min-w-0 flex-1">{oneLine(w.last_event_text ?? '', 220) || '—'}</span>
        {w.last_event_at && <span className="shrink-0 font-mono">{fmtTime(w.last_event_at)}</span>}
      </div>
    </div>
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

  const all = profiles.data?.profiles ?? []
  const active = profiles.data?.active ?? ''

  async function submit() {
    if (!title.trim() && !description.trim()) {
      setFail(new Error('标题或任务书至少填一个'))
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
    <Panel title="造一个人" extra={<UserPlus className="h-3.5 w-3.5 text-muted-foreground" />}>
      <div className="space-y-2.5">
        <Field label="标题" hint="一句话：这个人负责回答什么">
          <Input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="例如：核对 /admin 的越权读取面"
          />
        </Field>
        <Field label="任务书" hint="写清目标、授权范围、验收标准，以及需要引用的事实或证据">
          <Textarea
            rows={3}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={'普通用户能否读取他人订单？只做只读验证，不改数据；从事实 f_xxx 开始。'}
          />
        </Field>
        <Field
          label="模型绑定"
          hint={`项目档位：worker=${project?.worker_profile || '未配置'}，strong=${project?.strong_profile || '未配置'}；未配置的档位会由服务端报错`}
        >
          <Select value={mode} onValueChange={(v) => setMode(v as typeof mode)}>
            <SelectTrigger className="h-9 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="default" className="text-xs">
                默认（项目 worker 档 → 实例 active{active ? `：${active}` : ''}）
              </SelectItem>
              <SelectItem value="profile" className="text-xs">
                指定 profile
              </SelectItem>
              <SelectItem value="tier" className="text-xs">
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
              <SelectTrigger className="h-9 text-xs">
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
                      className="text-xs"
                      title={unavailable ? `${why}${p.probe_error ? `：${p.probe_error}` : ''}` : cap}
                    >
                      <span className="font-mono">{p.id}</span>
                      {p.tier ? (
                        <span className="ml-2 text-[10px] text-muted-foreground">[{p.tier}]</span>
                      ) : null}
                      <span className="ml-2 font-mono text-[10px] text-muted-foreground">容量 {cap}</span>
                      {p.model ? <span className="ml-2 text-[11px] text-muted-foreground">{p.model}</span> : null}
                      {why ? <span className="ml-2 text-[10px] text-destructive">{why}</span> : null}
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
              <SelectTrigger className="h-9 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="worker" className="text-xs">
                  worker（项目执行者默认配置）
                </SelectItem>
                <SelectItem value="strong" className="text-xs">
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
          <div className="flex items-center gap-1.5">
            <Input
              value={key}
              readOnly
              className="h-8 font-mono text-[11px]"
              aria-label="幂等键"
            />
            <Button
              size="xs"
              variant="outline"
              className="shrink-0"
              onClick={() => setKey(newIdempotencyKey())}
              title="换一个键：这会变成一个新的人"
            >
              重新生成
            </Button>
          </div>
        </Field>

        <div className="flex items-center gap-2">
          <Button size="sm" onClick={submit} disabled={busy}>
            <UserPlus className="h-3.5 w-3.5" />
            {busy ? '造人中…' : '造一个人'}
          </Button>
          <span className="text-[11px] text-muted-foreground">
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
    </Panel>
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
    <Panel title="默认模型" extra={<Layers className="h-3.5 w-3.5 text-muted-foreground" />}>
      <div className="space-y-2.5">
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

        {profiles.error && <Callout tone="destructive">读模型配置失败：{profiles.error}</Callout>}

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
            <SelectTrigger className="h-9 text-xs">
              <SelectValue placeholder="继承实例 active" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={INHERIT} className="text-xs">
                继承实例 active（清空）{active ? `：${active}` : ''}
              </SelectItem>
              {options.map((p) => (
                <SelectItem
                  key={p.id}
                  value={p.id}
                  disabled={p.disabled}
                  className="text-xs"
                  title={p.title || undefined}
                >
                  <span className="font-mono">{p.label}</span>
                  {p.note ? (
                    <span className={`ml-2 text-[10px] ${p.warn ? 'text-warning' : 'text-muted-foreground'}`}>
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
            <SelectTrigger className="h-9 text-xs">
              <SelectValue placeholder="继承实例 active" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={INHERIT} className="text-xs">
                继承实例 active（清空）{active ? `：${active}` : ''}
              </SelectItem>
              {options.map((p) => (
                <SelectItem
                  key={p.id}
                  value={p.id}
                  disabled={p.disabled}
                  className="text-xs"
                  title={p.title || undefined}
                >
                  <span className="font-mono">{p.label}</span>
                  {p.note ? (
                    <span className={`ml-2 text-[10px] ${p.warn ? 'text-warning' : 'text-muted-foreground'}`}>
                      {p.note}
                    </span>
                  ) : null}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" onClick={save} disabled={busy || !projectId}>
            {busy ? '保存中…' : '保存默认模型'}
          </Button>
          <span className="text-[11px] text-muted-foreground">
            保存只影响下一轮派发；已在跑的 run 不换模型
          </span>
        </div>
        {notice && <div className="text-[11px] text-success">{notice}</div>}
        <ErrorLine error={fail} />
      </div>
    </Panel>
  )
}

function PremisePolicyPanel() {
  const policy = useLoad(api.premisePolicy, [])
  const [choice, setChoice] = useState<PremisePolicy>('standard')
  const [busy, setBusy] = useState(false)
  const [fail, setFail] = useState<unknown>(null)
  const [notice, setNotice] = useState('')

  // The server row is the only source of truth: the editor mirrors what was
  // read, and a save only updates it with the server's response. No optimistic
  // "saved" state — the badge would otherwise lie while the PUT is in flight.
  useEffect(() => {
    if (!policy.data) return
	setChoice(policy.data.premise_policy)
  }, [policy.data])

  const saved = policy.data?.premise_policy
  const effective = policy.data?.effective
  const dirty = Boolean(policy.data) && choice !== saved

  async function save() {
    setBusy(true)
    setFail(null)
    setNotice('')
    try {
      const res = await api.savePremisePolicy(choice)
      policy.setData(res)
      setNotice(
        `已保存：前提政策 ${premisePolicyLabel(res.premise_policy)}（实际生效 ${premisePolicyLabel(res.effective)}）。只影响之后的创建/派发。`,
      )
    } catch (e) {
      setFail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Panel
      title="前提政策（硬前提门槛）"
      extra={<ShieldCheck className="h-3.5 w-3.5 text-muted-foreground" />}
    >
      <div className="space-y-2.5">
        <Field
          label="模式"
          hint="决定创建/派发方向时，对 from_fact_ids 的检查强度；引用/固定不受影响"
        >
          <Select
            value={choice}
            onValueChange={(v) => setChoice(v as PremisePolicy)}
            disabled={busy || !policy.data}
          >
            <SelectTrigger className="h-9 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="standard" className="text-xs">
                标准（默认）：前提存在且未被否决
              </SelectItem>
              <SelectItem value="strict" className="text-xs">
                严格：vuln/cred 前提必须已复核通过
              </SelectItem>
              <SelectItem value="verified_only" className="text-xs">
                证明优先：全部必要前提须独立核验
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        {policy.error && (
          <Callout tone="destructive">
            <div className="flex flex-wrap items-center gap-2">
              <span>读前提政策失败：{policy.error}</span>
              <Button size="xs" variant="outline" onClick={() => policy.reload()}>
                重试
              </Button>
            </div>
          </Callout>
        )}

        <div className="flex flex-wrap items-center gap-1.5 rounded-md border border-border bg-muted/40 px-2.5 py-1.5 text-[11px] text-muted-foreground">
          <span>
            当前保存值：
            <span className="font-medium text-foreground">
              {policy.data ? premisePolicyLabel(saved) : '…'}
            </span>
          </span>
          {effective && effective !== saved && (
            <span>
              · 实际生效：
              <span className="font-medium text-foreground">{premisePolicyLabel(effective)}</span>
            </span>
          )}
          {dirty && (
            <Badge size="sm" variant="warning">
              未保存
            </Badge>
          )}
        </div>

        <div className="text-[11px] leading-relaxed text-muted-foreground">
          {premisePolicyHint(policy.data ? choice : saved)}
        </div>

        {choice === 'strict' && (
          <Callout tone="warning">
            严格模式下，未复核（或复核绑定的是旧断言）的 vuln/cred 前提会让创建/派发被拒绝。这类事实可以改作引用（context_fact_ids）或固定（pinned_fact_ids），不要当硬前提。
          </Callout>
        )}

        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="xs"
            onClick={() => void save()}
            disabled={busy || !policy.data || !dirty}
          >
            {busy ? '保存中…' : '保存政策'}
          </Button>
          <span className="text-[11px] text-muted-foreground">
            只影响之后的创建/派发；已在跑的 run 不变
          </span>
        </div>

        {fail instanceof ApiError && fail.status === 400 && (
          <div className="text-[11px] text-muted-foreground">
            {fail.code === 'premise_policy_invalid'
              ? '取值不合法（premise_policy_invalid）：只能选“标准”或“严格”，服务端没有保存这次修改。'
              : '服务端拒绝了这次修改：请选择“标准”或“严格”后重试。'}
          </div>
        )}
        {fail instanceof ApiError && fail.status === 409 && (
          <div className="text-[11px] text-muted-foreground">
            {fail.code === 'premise_policy_unavailable'
              ? '这个页面没有绑定项目：前提政策是项目级设置，请在项目页设置，实例级不适用。'
              : '写入冲突：设置可能已被他人修改，刷新后重试，不要用旧值覆盖。'}
          </div>
        )}
        <ErrorLine error={fail} />
        {notice && <div className="text-[11px] text-success">{notice}</div>}
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
      extra={<ShieldCheck className="h-3.5 w-3.5 text-muted-foreground" />}
    >
      <div className="space-y-2.5">
        <Field
          label="模式"
          hint="open：同项目任意参与者可互发；allowlist：只允许下面列出的有向 from→to（commander 与发给自己除外）"
        >
          <Select value={mode} onValueChange={(v) => setMode(v as typeof mode)}>
            <SelectTrigger className="h-9 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="open" className="text-xs">
                open（默认，全项目可互发）
              </SelectItem>
              <SelectItem value="allowlist" className="text-xs">
                allowlist（只允许显式授权）
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        {peers.error && <Callout tone="destructive">读 peer 策略失败：{peers.error}</Callout>}

        {mode === 'allowlist' && (
          <div className="space-y-1.5">
            {rows.length === 0 && (
              <div className="rounded-md border border-border bg-muted/40 px-3 py-2 text-[12px] text-muted-foreground">
                allowlist 为空 = 除 commander 通道外都不能互发。用「加一条」授权方向。
              </div>
            )}
            {rows.map((r, i) => (
              <div key={i} className="flex items-center gap-1.5">
                <Select
                  value={r.from || undefined}
                  onValueChange={(v) => setRow(i, { from: v })}
                >
                  <SelectTrigger className="h-8 min-w-0 flex-1 text-xs">
                    <SelectValue placeholder="from" />
                  </SelectTrigger>
                  <SelectContent>
                    {options.map((p) => (
                      <SelectItem key={p.id} value={p.id} className="text-xs">
                        <span className="font-mono">{p.id}</span>
                        <span className="ml-2 text-[10px] text-muted-foreground">{p.title}</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <span className="shrink-0 text-[11px] text-muted-foreground">→</span>
                <Select value={r.to || undefined} onValueChange={(v) => setRow(i, { to: v })}>
                  <SelectTrigger className="h-8 min-w-0 flex-1 text-xs">
                    <SelectValue placeholder="to" />
                  </SelectTrigger>
                  <SelectContent>
                    {options.map((p) => (
                      <SelectItem key={p.id} value={p.id} className="text-xs">
                        <span className="font-mono">{p.id}</span>
                        <span className="ml-2 text-[10px] text-muted-foreground">{p.title}</span>
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
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
            <div className="flex flex-wrap items-center gap-2 pt-0.5">
              <Button size="xs" variant="outline" onClick={addRow} disabled={options.length === 0}>
                加一条
              </Button>
              {options.length === 0 && (
                <span className="text-[11px] text-muted-foreground">
                  还没有参与者：先造一个人，再设置 who→who。
                </span>
              )}
            </div>
          </div>
        )}

        <div className="flex items-center gap-2">
          <Button size="sm" onClick={save} disabled={busy || !peers.data}>
            {busy ? '保存中…' : '保存策略'}
          </Button>
          {notice && <span className="text-[11px] text-success">{notice}</span>}
        </div>
        <ErrorLine error={fail} />
      </div>
    </Panel>
  )
}
