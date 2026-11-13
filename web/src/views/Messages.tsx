import { useMemo, useRef, useState, type ReactNode } from 'react'
import { MessageSquare, RefreshCw, Send, Users } from 'lucide-react'
import { Badge, Button, Callout, EmptyState, ScrollArea, SkeletonList, ToggleGroup, ToggleGroupItem } from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { ChatComposer } from '@/components/ChatComposer'
import { InstructionsPanel } from '@/components/InstructionsPanel'
import {
  api,
  backlogSummary,
  isMechanicalSender,
  queueLoad,
  queueUnderPressure,
  readFactNotice,
} from '@/lib/api'
import { fmtBytes, fmtTime } from '@/lib/format'
import { usePoll } from '@/lib/hooks'
import {
  buildRecordMaps,
  describeRecord,
  eventLabel,
  messageKindLabel,
  participantName,
  type RecordMaps,
} from '@/lib/ioa'
import type { CommunicationQueue, CommunicationStatus, IOARecord, Message, Participant } from '@/lib/types'

/** Delivery state as the operator sees it. "accepted" is IOA taking the bytes,
 *  not the peer's model reading them; only an ack becomes "confirmed". */
function deliveryVariant(status: string): 'success' | 'info' | 'warning' | 'danger' | 'muted' {
  switch (status) {
    case 'confirmed':
      return 'success'
    case 'accepted':
      return 'info'
    case 'queued':
    case 'sending':
      return 'warning'
    case 'unknown':
      return 'danger'
    default:
      return 'muted'
  }
}

function deliveryShortLabel(status: string): string {
  switch (status) {
    case 'queued':
      return '排队'
    case 'sending':
      return '投递中'
    case 'accepted':
      return '已接收'
    case 'confirmed':
      return '已确认'
    case 'unknown':
      return '结果未知'
    default:
      return status || '—'
  }
}

function deliveryLabel(status: string): string {
  switch (status) {
    case 'queued':
      return '排队（对方下次运行可见）'
    case 'sending':
      return '投递中（不能撤回）'
    case 'accepted':
      return '宿主已接收（未确认已读）'
    case 'confirmed':
      return '对方已确认'
    case 'unknown':
      return '结果未知（不要自动重发）'
    default:
      return status
  }
}

type ConversationTab = 'native' | 'project' | 'instructions'

/** Project conversation. It is a record of participants talking, not a fact
 *  feed: messages never enter the graph or the report. The operator types here
 *  like a chat; every send is one delivery per recipient, and the receipt says
 *  who got it now, who reads it next round, and which settled direction was
 *  deliberately skipped. */
export function Messages() {
  const [tab, setTab] = useState<ConversationTab>('project')
  const messages = usePoll(api.messages, 4000)
  const participants = usePoll(api.participants, 8000)
  // Queue pressure is observation, not delivery: a deferred notice means the
  // router kept the change in the journal instead of dropping it.
  const communication = usePoll(api.communication, 8000)
  // The composer's paused/running hints come from the same board the graph dock
  // uses; a paused project queues one-shot messages and saves instructions.
  const board = usePoll(api.board, 8000)
  const [directiveSeq, setDirectiveSeq] = useState(0)
  const composerRef = useRef<HTMLDivElement | null>(null)
  const rows = messages.data ?? []
  const people = participants.data ?? []
  const paused = Boolean(board.data?.project_paused || board.data?.decider.paused)
  const runningExecutors = board.data?.intents.running ?? 0

  function focusComposer() {
    const el = composerRef.current
    el?.scrollIntoView({ block: 'nearest' })
    el?.querySelector<HTMLTextAreaElement>('textarea')?.focus()
  }

  return (
    <Page
      title="通信"
      subtitle="项目对话、持久指令与 IOA 记录；投递或确认均不代表动作已执行"
      actions={
        <Button size="sm" variant="outline" onClick={focusComposer}>
          <Send className="h-3.5 w-3.5" />
          下发
        </Button>
      }
      scroll={false}
    >
      <div className="grid h-full min-h-0 grid-cols-1 gap-4 overflow-y-auto p-4 sm:p-5 md:grid-cols-[280px_minmax(0,1fr)] md:overflow-hidden xl:p-6">
        {/* Left: the session roster (who is on the channel) + queue pressure. */}
        <div className="flex min-h-0 flex-col gap-4 md:overflow-y-auto">
          <Panel
            title={`参与者（${people.length}）`}
            className="flex min-h-0 flex-col"
            bodyClassName="min-h-0 flex-1 overflow-y-auto p-1.5"
            extra={
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label="刷新参与者"
                title="刷新参与者"
                onClick={() => participants.reload()}
              >
                <RefreshCw className={`h-3.5 w-3.5 ${participants.loading ? 'animate-spin' : ''}`} />
              </Button>
            }
          >
            {participants.loading && !participants.data && <SkeletonList count={4} />}
            {participants.error && (
              <div className="p-1">
                <Callout tone="destructive">
                  <div className="flex flex-wrap items-center gap-2">
                    <span>读参与者失败：{participants.error}</span>
                    <Button size="xs" variant="outline" onClick={() => participants.reload()}>重试</Button>
                  </div>
                </Callout>
              </div>
            )}
            {!participants.loading && !participants.error && people.length === 0 && (
              <EmptyState
                compact
                icon={Users}
                title="还没有参与者"
                description="造一个工作者或开始一轮运行后，参与者会出现在这里。"
              />
            )}
            {people.map((p) => (
              <ParticipantRow key={p.id} p={p} />
            ))}
          </Panel>
          <CommunicationBacklog status={communication.data} error={communication.error} people={people} onRetry={() => communication.reload()} />
        </div>

        <div className="flex min-h-0 flex-col gap-4">
          <Panel
            title={tab === 'native' ? '原生 IOA 记录' : tab === 'instructions' ? '持久指令' : '对话'}
            className="flex min-h-0 flex-1 flex-col"
            bodyClassName="flex min-h-0 flex-1 flex-col p-0"
            extra={
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label="刷新通信记录"
                title="刷新通信记录"
                onClick={() => { messages.reload(); communication.reload() }}
              >
                <RefreshCw className={`h-3.5 w-3.5 ${messages.loading ? 'animate-spin' : ''}`} />
              </Button>
            }
          >
            {/* One 12px toolbar: which record source to read. */}
            <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline px-3 py-2">
              <ToggleGroup
                value={tab}
                onValueChange={(next) => setTab(next === 'native' ? 'native' : next === 'instructions' ? 'instructions' : 'project')}
                ariaLabel="通信记录来源"
              >
                <ToggleGroupItem value="project" className="h-8 px-2.5 text-[12px]">
                  对话 <span className="tw-nums font-mono">{rows.length}</span>
                </ToggleGroupItem>
                <ToggleGroupItem value="instructions" className="h-8 px-2.5 text-[12px]">持久指令</ToggleGroupItem>
                <ToggleGroupItem value="native" className="h-8 px-2.5 text-[12px]">原生 IOA</ToggleGroupItem>
              </ToggleGroup>
              <span className="ml-auto text-[11px] text-ink-3">
                {tab === 'project' ? '一条 = 一次投递，不代表已读' : tab === 'native' ? '观察到消息不代表对方已读' : '下发框选择「持续到撤销」创建'}
              </span>
            </div>
            {tab === 'instructions' ? <InstructionsPanel key={directiveSeq} /> : tab === 'native' ? <NativeConversation people={people} /> : (
              <ProjectConversation rows={rows} people={people} error={messages.error} loading={messages.loading} onRetry={() => messages.reload()} />
            )}
          </Panel>
          {/* The single 下发 entry: the same component as the graph's right dock.
              A successful directive refreshes the feed and remounts the
              management panel so a new persistent instruction shows up. */}
          <div ref={composerRef} className="max-h-[48vh] shrink-0 overflow-y-auto rounded-xl border border-hairline bg-card shadow-soft">
            <ChatComposer
              paused={paused}
              runningExecutors={runningExecutors}
              onSent={() => {
                messages.reload()
                board.reload()
                setDirectiveSeq((current) => current + 1)
              }}
            />
          </div>
        </div>
      </div>
    </Page>
  )
}

/** Per-recipient queue pressure and routing checkpoints. "Deferred" means the
 *  router left a change in the journal because that queue was at its bound: the
 *  change is retried, not lost. The panel is deliberately small — it reports
 *  pressure, it is not a second inbox. */
function CommunicationBacklog({ status, error, people, onRetry }: {
  status?: CommunicationStatus
  error?: string
  people: Participant[]
  onRetry: () => void
}) {
  const summary = backlogSummary(status)
  const limits = { max_messages: status?.max_messages ?? 0, max_bytes: status?.max_bytes ?? 0 }
  const visible = (status?.queues ?? []).filter((q) => q.pending > 0 || q.deferred)
  return (
    <Panel title="通信积压" className="shrink-0" bodyClassName="space-y-2 p-3">
      {error && (
        <Callout tone="destructive">
          <div className="flex flex-wrap items-center gap-2">
            <span>读通信积压失败：{error}</span>
            <Button size="xs" variant="outline" onClick={onRetry}>重试</Button>
          </div>
        </Callout>
      )}
      <p className="text-[12px] leading-5 text-ink-2">
        排队 <span className="tw-nums font-mono">{summary.queued}</span> 条 · <span className="tw-nums font-mono">{fmtBytes(summary.bytes)}</span> · <span className="tw-nums font-mono">{summary.recipients}</span> 个收件人
        {summary.deferred > 0 && (
          <> · <span className="text-warning">{summary.deferred} 个队列延后待发</span></>
        )}
      </p>
      {status?.paused && (
        <p className="text-[11px] leading-4 text-warning">
          项目暂停：关联通知只入队不投递，恢复后由活动会话或下一次提示词读取。
        </p>
      )}
      {!status && <p className="text-[11px] text-ink-3">读取中…</p>}
      {status && visible.length === 0 && <p className="text-[11px] text-ink-3">没有积压</p>}
      {visible.slice(0, 8).map((q) => <QueueRow key={q.recipient} q={q} limits={limits} people={people} />)}
      {status && (
        <p className="text-[11px] leading-4 text-ink-3">
          上限 {status.max_messages} 条 / {fmtBytes(status.max_bytes)}；满队列时发送返回 429 message_backpressure，改动不会被静默丢弃。
        </p>
      )}
    </Panel>
  )
}

function QueueRow({ q, limits, people }: {
  q: CommunicationQueue
  limits: { max_messages: number; max_bytes: number }
  people: Participant[]
}) {
  const load = queueLoad(q, limits)
  const pressure = queueUnderPressure(q, limits)
  return (
    <div data-queue-recipient={q.recipient} className="space-y-1 rounded-md border border-hairline px-2.5 py-1.5 text-[11px]">
      <div className="flex items-center gap-1.5">
        <span className="min-w-0 truncate text-[12px] text-ink-2" title={q.recipient}>{participantName(q.recipient, people)}</span>
        {q.deferred && (
          <Badge size="sm" variant="warning" title="队列已满：变化留在日志，检查点未推进，稍后重试；不会丢弃">
            延后
          </Badge>
        )}
        {pressure && !q.deferred && (
          <Badge size="sm" variant="primary" title="接近容量上限">接近上限</Badge>
        )}
        <span className="tw-nums ml-auto shrink-0 font-mono text-[11px] text-ink-3">
          {q.pending} 条 · {fmtBytes(q.bytes)}
        </span>
      </div>
      <div className="h-1 overflow-hidden rounded-full bg-surface-2" title={`容量占用 ${Math.round(load * 100)}%`}>
        <div
          className={pressure ? 'h-full bg-warning' : 'h-full bg-info/60'}
          style={{ width: `${Math.max(2, Math.round(load * 100))}%` }}
        />
      </div>
      <div className="flex items-center gap-2 font-mono text-[11px] text-ink-3">
        <span title="路由检查点（日志序号）不是已读回执">cursor {q.cursor}</span>
        {q.oldest && <span title={q.oldest}>最早 {fmtTime(q.oldest).slice(5)}</span>}
      </div>
    </div>
  )
}


function NativeConversation({ people }: { people: Participant[] }) {
  const [instance, setInstance] = useState(false)
  return <div className="flex h-full min-h-0 flex-col">
    <label className="flex shrink-0 items-center gap-2 border-b border-hairline px-3 py-2 text-[11px] text-ink-3">
      <input
        type="checkbox"
        className="h-3.5 w-3.5 accent-primary"
        checked={instance}
        onChange={e => setInstance(e.target.checked)}
      />
      查看整个实例（包含其他项目和未关联会话）
    </label>
    <NativeRecords key={instance ? 'instance' : 'project'} instance={instance} people={people} />
  </div>
}

function NativeRecords({ instance, people }: { instance: boolean; people: Participant[] }) {
  const observed = usePoll(instance ? api.instanceIOAObservations : api.ioaObservations, 4000)
  const records = observed.data?.records ?? []
  const sessions = observed.data?.sessions ?? {}
  const maps = useMemo(() => buildRecordMaps(sessions, people, records), [sessions, people, records])
  return <div className="flex min-h-0 flex-1 flex-col">
    <div className="shrink-0 space-y-1.5 border-b border-hairline px-3 py-2 text-[11px] leading-5 text-ink-3">
      <p>{instance ? '实例最近 500 条观察，包含其他项目和未关联会话' : '本项目已关联会话的最近 500 条观察'} · {records.length} 条 · 观察到消息不代表对方已读</p>
      <details className="rounded-md border border-hairline bg-surface-2/40 px-2.5 py-1.5">
        <summary className="cursor-pointer select-none text-ink-2 hover:text-ink">这些记录怎么读？</summary>
        <ul className="mt-1 space-y-0.5 pl-0.5">
          <li><span className="text-ink-2">一条卡片 = IOA 里的一次投递原始记录</span>；「记录详情」里是 IOA 消息 id、发送节点和原始会话 id。</li>
          <li><span className="text-ink-2">方向</span>：左边是来源、右边是接收方，标题已还原成会话/参与者名称；名字下面一行写明地址来源。显示「控制台直投」表示控制台经 IOA 直接送进对方会话，IOA 记录里没有来源会话，发送方以消息头的 <code>from=</code> 为准，不是丢数据。</li>
          <li><span className="text-ink-2">[collab] 开头</span>：天吴加给接收模型的协作头（<code>from</code>/<code>to</code>/<code>kind</code>/<code>message_id</code>）。控制台已经把它拆成字段，正文从换行之后开始。</li>
          <li><span className="text-ink-2">会话短串</span>（如 <code>9da4e9bb76…</code>）：接收方 harness 会话 id 前 10 位，只用于和「运行」页核对，<em>不是</em>消息编号；消息编号是 <code>m_xxxxxxxx</code>。</li>
        </ul>
      </details>
    </div>
    {observed.error && <div className="px-3 pt-2"><Callout tone="destructive">{observed.error}</Callout></div>}
    {observed.loading && records.length === 0 && <div className="p-2"><SkeletonList count={4} /></div>}
    {records.length === 0 && !observed.loading && !observed.error && (
      <EmptyState
        compact
        title="暂无 IOA 观察"
        description="运行中的原生会话消息会出现在这里；观察到消息不代表对方已读。"
      />
    )}
    <ScrollArea className="min-h-0 flex-1">
      <div className="space-y-2 p-3">{records.map(rec => <NativeMessage key={rec.ioa_message_id || rec.seq} rec={rec} maps={maps} />)}</div>
    </ScrollArea>
  </div>
}

/** One raw IOA observation, rendered as direction + addressing facts + body. */
function NativeMessage({ rec, maps }: { rec: IOARecord; maps: RecordMaps }) {
  const env = rec.envelope
  const view = describeRecord(rec, maps)
  const event = env?.event ?? ''
  const chips: { key: string; node: ReactNode }[] = [
    { key: 'source', node: <span data-ioa-source-note>来源：{view.fromDetail}</span> },
    { key: 'target', node: <span data-ioa-target-note>目标：{view.toDetail}</span> },
  ]
  if (view.peer?.kind) {
    chips.push({
      key: 'kind',
      node: <span title={`消息语义由发送方声明：kind=${view.peer.kind}`}>类型 {messageKindLabel(view.peer.kind)}</span>,
    })
  }
  if (view.peer?.messageId) {
    chips.push({
      key: 'message',
      node: <span className="font-mono" title="项目消息编号；「项目消息」页签里是同一个号">{view.peer.messageId}</span>,
    })
  }
  if (view.peer?.inReplyTo) {
    chips.push({
      key: 'reply',
      node: <span>回复 <span className="font-mono">{view.peer.inReplyTo}</span></span>,
    })
  }
  if ((view.peer?.facts.length ?? 0) > 0) {
    chips.push({
      key: 'facts',
      node: <span title={(view.peer?.facts ?? []).join('\n')}>引用事实 {view.peer?.facts.length} 条</span>,
    })
  }
  if (env?.agent?.name) {
    chips.push({ key: 'agent', node: <span title={`子代理 ${env.agent.name}${env.agent.type ? ` · ${env.agent.type}` : ''}`}>子代理 {env.agent.name}</span> })
  }
  if (env?.call_id) {
    chips.push({ key: 'call', node: <span className="font-mono" title="派发调用 id">调用 {env.call_id}</span> })
  }
  return <article data-ioa-seq={rec.seq} data-ioa-event={event} className="overflow-hidden rounded-lg border border-hairline bg-card/40">
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 pt-2.5">
      <Badge size="sm" variant={event === 'message' ? 'info' : 'muted'} title="IOA 协议事件类型">{eventLabel(event)}</Badge>
      <span
        data-ioa-endpoint="source"
        title={view.from?.hint}
        className={`min-w-0 text-[12px] font-medium ${view.from ? 'text-info' : 'italic text-ink-3'}`}
      >{view.from?.label ?? '来源未标注'}</span>
      <span className="text-ink-3" aria-hidden>→</span>
      <span
        data-ioa-endpoint="target"
        title={view.to?.hint}
        className={`min-w-0 text-[12px] font-medium ${view.to ? 'text-info' : 'italic text-ink-3'}`}
      >{view.to?.label ?? '目标未标注'}</span>
      {env?.interrupt && <Badge size="sm" variant="warning" title="要求中断对方当前模型请求后立即投递">中断式投递</Badge>}
      {env?.status && <Badge size="sm" variant="muted" title="harness 报告的投递状态">{env.status}</Badge>}
      <time className="tw-nums ml-auto shrink-0 font-mono text-[11px] text-ink-3" title={rec.created_at || rec.observed_at}>{fmtTime(rec.created_at || rec.observed_at)}</time>
    </div>
    <div className="flex flex-wrap items-center gap-x-1.5 gap-y-1 px-3 pt-1 text-[11px] text-ink-3">
      {chips.map((chip, i) => (
        <span key={chip.key} className="flex items-center gap-1.5">{i > 0 && <span aria-hidden className="text-ink-3/50">·</span>}{chip.node}</span>
      ))}
    </div>
    <div data-ioa-body className="tw-break mx-3 my-2 whitespace-pre-wrap rounded-r-md border-l-2 border-info/50 bg-surface-2/40 px-3 py-2 text-[12.5px] leading-relaxed text-ink-2">{view.body || '（无正文）'}</div>
    <details data-ioa-details className="border-t border-hairline px-3 py-1.5 text-[11px] text-ink-3">
      <summary className="cursor-pointer select-none hover:text-ink">记录详情（原始 id 与时间）</summary>
      <div className="mt-1 space-y-0.5">
        <DetailLine label="IOA 消息" value={rec.ioa_message_id || '—'} hint="IOA 传输层的消息 id，与项目消息编号不同" />
        {env?.id && <DetailLine label="事件 id" value={env.id} hint="应用层事件 id；同一消息的重试复用它，便于去重" />}
        {rec.sender && <DetailLine label="发送节点" value={rec.sender} hint="IOA 节点指纹（控制台/宿主各有一个），不是参与者 id" />}
        {env?.source?.session_id && <DetailLine label="来源会话" value={env.source.session_id} />}
        {env?.target?.session_id && <DetailLine label="目标会话" value={env.target.session_id} />}
        {env?.call_id && <DetailLine label="调用" value={env.call_id} />}
        {rec.created_at && <DetailLine label="创建时间" value={fmtTime(rec.created_at)} />}
        <DetailLine label="观察时间" value={fmtTime(rec.observed_at)} />
      </div>
    </details>
    {rec.error && <div className="px-3 pb-2"><Callout tone="destructive">{rec.error}</Callout></div>}
  </article>
}

function DetailLine({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="flex gap-2">
      <span className="w-16 shrink-0 text-ink-3/80">{label}</span>
      <span className="tw-break min-w-0 flex-1 font-mono" title={hint ?? value}>{value}</span>
    </div>
  )
}

function ParticipantRow({ p }: { p: Participant }) {
  return (
    <div
      className="mb-0.5 rounded-lg px-2.5 py-2 transition-colors hover:bg-surface-2 focus-within:bg-surface-2/60"
      data-participant-id={p.id}
    >
      <div className="flex items-center gap-2">
        <span
          className={`inline-block h-1.5 w-1.5 shrink-0 rounded-full ${p.contactable ? 'bg-success' : 'bg-ink-3/50'}`}
          title={p.contactable ? '可联系' : '已结束，需显式重开'}
        />
        <span className="min-w-0 truncate text-[12px] font-medium text-ink">{p.title}</span>
        {p.kind === 'commander' && <Badge size="sm" variant="secondary">指挥官</Badge>}
        <span className="tw-nums ml-auto shrink-0 font-mono text-[11px] text-ink-3">#{p.generation ?? 0}</span>
      </div>
      <div className="mt-0.5 truncate font-mono text-[11px] text-ink-3">{p.id}</div>
      <div className="flex flex-wrap gap-1.5 font-mono text-[11px] text-ink-3">
        {p.status ? <span>{p.status}</span> : null}
        {p.profile ? <span className="truncate">· {p.profile}</span> : null}
        {p.model ? <span className="truncate">· {p.model}</span> : null}
      </div>
      {p.note && <div className="mt-0.5 text-[11px] leading-4 text-ink-3">{p.note}</div>}
    </div>
  )
}

function ProjectConversation({ rows, people, error, loading, onRetry }: {
  rows: Message[]
  people: Participant[]
  error?: string
  loading: boolean
  onRetry: () => void
}) {
  return <div className="flex h-full min-h-0 flex-col">
    <div className="shrink-0 space-y-1.5 border-b border-hairline px-3 py-2 text-[11px] leading-5 text-ink-3">
      <p>项目对话：一条 = 一次投递，不代表对方已读，也不代表结论；事实与证据在「战场」里。下发入口已移到「图谱」右侧（画布右上角的对话按钮），这里保留完整对话记录。</p>
      <details className="rounded-md border border-hairline bg-surface-2/40 px-2.5 py-1.5">
        <summary className="cursor-pointer select-none text-ink-2 hover:text-ink">投递状态怎么读？</summary>
        <ul className="mt-1 space-y-0.5 pl-0.5">
          <li><span className="text-ink-2">排队</span>：对方下次运行开始时可见</li>
          <li><span className="text-ink-2">投递中</span>：已经发出，不能撤回</li>
          <li><span className="text-ink-2">已接收</span>：宿主收下了，但未确认对方模型已读</li>
          <li><span className="text-ink-2">已确认</span>：对方给了确认回执</li>
          <li><span className="text-ink-2">结果未知</span>：不确定是否送达，不要自动重发</li>
        </ul>
      </details>
    </div>
    {error && (
      <div className="px-2 pt-2">
        <Callout tone="destructive">
          <div className="flex flex-wrap items-center gap-2">
            <span>读对话失败：{error}</span>
            <Button size="xs" variant="outline" onClick={onRetry}>重试</Button>
          </div>
        </Callout>
      </div>
    )}
    {loading && rows.length === 0 && <div className="p-2"><SkeletonList count={5} /></div>}
    {rows.length === 0 && !loading && !error && (
      <EmptyState
        compact
        icon={MessageSquare}
        title="还没有消息"
        description="到「图谱」右侧的下方面板发第一条：只交给指挥官，或同时发给全体执行者。"
      />
    )}
    <ScrollArea className="min-h-0 flex-1">
      <div className="divide-y divide-hairline p-2">
        {rows.map((m) => (
          <MessageRow key={m.id} m={m} people={people} />
        ))}
      </div>
    </ScrollArea>
  </div>
}

function MessageRow({ m, people }: { m: Message; people: Participant[] }) {
  const notice = readFactNotice(m)
  const mechanical = isMechanicalSender(m.from_participant_id)
  const sender = mechanical ? '机械通知' : participantName(m.from_participant_id, people)
  return (
    <article
      data-message-id={m.id}
      data-mechanical-notice={mechanical ? 'true' : undefined}
      className={`px-2 py-2.5 transition-colors ${mechanical ? 'bg-surface-2/20' : 'hover:bg-surface-2/40'}`}
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span
          className={`text-[12px] font-medium ${mechanical ? 'text-ink-3' : 'text-info'}`}
          title={mechanical ? '控制面关联路由写入的机械通知，不是指挥官、操作员或执行者在说话' : `参与者 ${m.from_participant_id}`}
        >
          {sender}
        </span>
        {mechanical && <Badge size="sm" variant="outline" className="font-mono" title="机械发送者身份：没有模型会话，也不代表授权">ledger</Badge>}
        <span className="text-ink-3" aria-hidden>→</span>
        <span className="text-[12px] font-medium text-info" title={`参与者 ${m.to_participant_id}`}>{participantName(m.to_participant_id, people)}</span>
        {m.kind && (
          <Badge size="sm" variant="muted" title={`消息语义由发送方声明：kind=${m.kind}`}>
            {m.kind === 'fact_notice' ? '共享发现通知' : messageKindLabel(m.kind)}
          </Badge>
        )}
        <Badge size="sm" variant={deliveryVariant(m.status)} title={deliveryLabel(m.status)}>{deliveryShortLabel(m.status)}</Badge>
        {(m.redeliveries ?? 0) > 0 && (
          <Badge
            size="sm"
            variant="warning"
            title={`重试已重新注入这条未确认消息 ${m.redeliveries} 次`}
          >
            重投 {m.redeliveries}
          </Badge>
        )}
        {m.delivery_path && (
          <span className="text-[11px] text-ink-3" title="投递路径：运行中=说进对方正在跑的会话；下一轮=随对方下次运行的提示词注入">
            {m.delivery_path === 'ioa' ? '运行中投递' : '下一轮注入'}
          </span>
        )}
        <time className="tw-nums ml-auto shrink-0 font-mono text-[11px] text-ink-3" title={m.created_at}>{fmtTime(m.created_at).slice(5)}</time>
      </div>
      <div className={`tw-break mt-1.5 whitespace-pre-wrap rounded-r-md border-l-2 bg-surface-2/40 px-2.5 py-1.5 text-[12px] leading-relaxed text-ink-2 ${mechanical ? 'border-ink-3/40' : 'border-info/30'}`}>
        {m.text}
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 font-mono text-[11px] text-ink-3">
        <span title="项目消息编号">{m.id}</span>
        {m.notice_intent_id && (
          <span data-task-notice={m.notice_intent_id}>
            来源任务 <span className="font-mono">{m.notice_intent_id}</span>
            {m.notice_run_id && <> · 运行 <span className="font-mono">{m.notice_run_id}</span></>}
            {' · '}交给承接者判断，不改写原任务的必要前提。
          </span>
        )}
        {!notice && m.in_reply_to && <span>回复 <span className="font-mono">{m.in_reply_to}</span></span>}
        {!notice && (m.fact_refs?.length ?? 0) > 0 && (
          <span className="flex flex-wrap items-center gap-1" title={(m.fact_refs ?? []).join('\n')}>
            引用事实
            {m.fact_refs?.map((f) => (
              <Badge key={f} size="sm" variant="outline" className="font-mono">
                {f}
              </Badge>
            ))}
          </span>
        )}
      </div>
      {notice && (
        <div
          data-fact-notice-fact={notice.factId}
          data-fact-notice-version={notice.version}
          data-fact-notice-coalesced={notice.coalesced}
          className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-ink-3"
        >
          <span>事实 <span className="font-mono text-ink-2">{notice.factId}</span></span>
          <span aria-hidden className="text-ink-3/50">·</span>
          <span>记录版本 <span className="font-mono">v{notice.version}</span></span>
          {notice.coalesced > 0 && (
            <>
              <span aria-hidden className="text-ink-3/50">·</span>
              <Badge size="sm" variant="muted" title="同一事实的后续变化已合并进这条排队通知，没有产生重复行">
                合并 {notice.coalesced} 次
              </Badge>
            </>
          )}
          <span aria-hidden className="text-ink-3/50">·</span>
          <span title="通知只携带指针；正文、版本与证据在事实详情里按 ID 读取">按 ID 读取记录（通知不是正文，也不授权动作）</span>
        </div>
      )}
      {m.error && <div className="mt-1 text-[11px] text-destructive">{m.error}</div>}
    </article>
  )
}
