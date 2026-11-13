// The pinned node inspector (moved verbatim out of GraphView.tsx by T6, docs/33
// §4.6). It is loaded with React.lazy so the ~800 lines of fact/intent detail
// actions do not sit in the GraphView chunk. The logic below is unchanged: this
// file only owns the panel, the page shell owns selection.

import { useMemo, useState } from 'react'
import {
  Activity,
  Download,
  Eye,
  GitBranch,
  MessageSquare,
  Play,
  RefreshCw,
  RotateCcw,
  Send,
  ShieldCheck,
  ShieldX,
  X,
} from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Input,
  ScrollArea,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  StatusIndicator,
} from '@cyber/ui'
import { EvidencePreview } from '@/components/EvidencePreview'
import { FactClaimDetail } from '@/components/FactClaimDetail'
import { IntentContextEditor } from '@/components/IntentContextEditor'
import { RetryIntentDialog } from '@/components/RetryIntentDialog'
import { RunEvidenceView } from '@/components/RunEvidenceView'
import { api } from '@/lib/api'
import { evidencePreviewFile, type PreviewFile } from '@/lib/evidence-preview'
import { fmtBytes, fmtInt, fmtTime, kindVariant, statusKind, statusLabel } from '@/lib/format'
import { useLoad, usePoll } from '@/lib/hooks'
import { latestExecutorRun } from '@/lib/run-evidence'
import type { GraphNode } from '@/graph/model'
import type { Fact, Run } from '@/lib/types'

export function GraphNodePanel(props: Parameters<typeof NodeWorkspace>[0]) {
  // Detail reads, drafts and confirmation dialogs belong to one selected
  // node. Retire them together before rendering a different record.
  return <NodeWorkspace key={props.node.id} {...props} />
}

function NodeWorkspace({
  node,
  facts,
  runs,
  onSelectFact,
  onClose,
  onChanged,
  childRefresh,
  onChildIntent,
}: {
  node: GraphNode
  /** The graph's fact poll: resolves a goal's cited fact ids to titles. */
  facts: Fact[]
  /** The run poll: a direction's frozen input versions live on its latest run. */
  runs: Run[]
  /** Select a fact node (`f:<id>`) when a cited fact is clicked. */
  onSelectFact: (factId: string) => void
  onClose: () => void
  onChanged: () => void
  /** Changes after a child is created; re-reads the child list. */
  childRefresh: number
  /** Open the "hang a child under this direction" dialog for this intent. */
  onChildIntent: (parent: { id: string; title: string }) => void
}) {
  const isFact = node.type === 'fact'
  const factID = node.meta?.fact?.id ?? ''
  const detail = useLoad(() => (isFact && factID ? api.fact(factID) : Promise.resolve(null)), [factID])
  const children = useLoad(
    () => (node.meta?.intent?.id ? api.children(node.meta.intent.id) : Promise.resolve([])),
    [node.meta?.intent?.id, childRefresh],
  )
  const usage = useLoad(
    () => (node.meta?.intent?.id ? api.intentUsage(node.meta.intent.id) : Promise.resolve(null)),
    [node.meta?.intent?.id],
  )
  const profiles = useLoad(api.profiles, [])
  const premisePolicy = useLoad(api.premisePolicy, [])
  const readiness = usePoll(
    () => node.meta?.intent?.id ? api.intentReadiness(node.meta.intent.id) : Promise.resolve(null),
    10000,
    [node.meta?.intent?.id, childRefresh],
  )
  const [handoffOpen, setHandoffOpen] = useState(false)
  const [handoffProfile, setHandoffProfile] = useState('')
  const [handoffNote, setHandoffNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  const [parking, setParking] = useState(false)
  const [retrying, setRetrying] = useState(false)
  const [rejecting, setRejecting] = useState(false)
  const [reason, setReason] = useState('')
  const [intervening, setIntervening] = useState(false)
  const [interveneMode, setInterveneMode] = useState<'say' | 'steer'>('say')
  const [interveneText, setInterveneText] = useState('')
  const [refFact, setRefFact] = useState('')
  const [interrupt, setInterrupt] = useState(false)
  // Evidence is opened in the shared preview sheet, like the evidence page: the
  // detail panel is where you decide "is this the proof?", not where you save it.
  const [preview, setPreview] = useState<PreviewFile | null>(null)

  const intent = node.meta?.intent
  const goal = node.meta?.goal
  const fact = node.meta?.fact
  // What this direction's latest executor run actually read, frozen at dispatch.
  const latestRun = intent ? latestExecutorRun(runs, intent.id) : undefined

  // The seal's provenance: the facts it cited when the server recorded them;
  // else (legacy seal) the conclusion facts that carry the sealed evidence —
  // a bank case's e_… is not a fact, f_… is where it actually landed.
  const goalRefs = useMemo((): { legacy: boolean; items: { id: string; fact?: Fact }[] } => {
    if (!goal) return { legacy: false, items: [] }
    const byId = new Map(facts.map((f) => [f.id, f]))
    if (goal.from && goal.from.length > 0) {
      return { legacy: false, items: goal.from.map((id) => ({ id, fact: byId.get(id) })) }
    }
    const eid = goal.evidence_id
    if (goal.met && eid) {
      return {
        legacy: true,
        items: facts
          .filter((f) => (f.evidence_ids ?? []).includes(eid))
          .map((f) => ({ id: f.id, fact: f })),
      }
    }
    return { legacy: false, items: [] }
  }, [goal, facts])

  async function run(fn: () => Promise<unknown>, done: string) {
    setBusy(true)
    setNote('')
    try {
      await fn()
      setNote(done)
      onChanged()
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  async function sendIntervene() {
    if (!intent || !interveneText.trim()) return
    setBusy(true)
    setNote('')
    try {
      if (interveneMode === 'say') {
        const r = await api.sayIntent(intent.id, interveneText.trim(), interrupt, refFact.trim())
        setNote(`已投递（IOA 接受，${r.message_id}）`)
      } else {
        const r = await api.steerIntent(intent.id, interveneText.trim(), refFact.trim())
        setNote(`已中断并续话（新 run ${r.run_id}）`)
      }
      setInterveneText('')
      setRefFact('')
      setIntervening(false)
      onChanged()
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  function openIntervene() {
    setIntervening((v) => !v)
  }

  async function doHandoff() {
    if (!intent || !handoffProfile) return
    setBusy(true)
    setNote('')
    try {
      const r = await api.handoffIntent(
        intent.id,
        handoffProfile,
        handoffNote.trim(),
        intent.handoff_version ?? 0,
      )
      setNote(`已换模型接班：${handoffProfile}（新 run ${r.run_id}）。旧会话日志保留，新会话从交接继续。`)
      setHandoffOpen(false)
      setHandoffNote('')
      onChanged()
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    // Pinned inspector: a definite top+bottom (not max-height) is what lets the
    // inner scroll area shrink and scroll. A max-height on an auto-height flex
    // column only clamps the box — the content kept its height and was simply
    // clipped, so a long fact had unreachable rows and no wheel scrolling.
    <div className="absolute bottom-12 right-3 top-[56px] flex w-[360px] flex-col overflow-hidden rounded-lg border border-border bg-card/95 shadow-elevated backdrop-blur">
      <div className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-border px-3 py-2">
        <Badge size="sm" variant={isFact ? kindVariant(fact?.kind) : 'primary'}>
          {node.type === 'fact' ? (fact?.kind ?? 'fact') : node.type === 'step' ? 'intent' : node.type}
        </Badge>
        {node.attempts && node.attempts > 1 && (
          <Badge size="sm" variant="muted" title="这条方向的尝试次数（重试/续跑会累加）">
            ×{node.attempts}
          </Badge>
        )}
        {node.status && isnStatus(node.status) && (
          <StatusIndicator compact status={statusKind(node.status)} label={statusLabel(node.status)} className="shrink-0" />
        )}
        <Button
          size="icon-xs"
          variant="ghost"
          className="ml-auto shrink-0"
          onClick={onClose}
          aria-label="关闭"
        >
          <X className="h-3.5 w-3.5" />
        </Button>
      </div>

      {/* Radix ScrollArea sizes its viewport as a CSS table so horizontal scroll
          can work; with a controller-only panel every long row then widened the
          table to its max-content width (a single review note made the content
          15k px wide) and the panel showed a clipped slice of it. The panel only
          scrolls vertically, so lay the ScrollArea content out as a block: it
          stays 100% of the 360px panel and text wraps. */}
      <ScrollArea className="min-h-0 flex-1 [&_[data-radix-scroll-area-viewport]>div]:!block">
        <div className="min-w-0 space-y-3 p-3 text-[12px] leading-[1.55]">
          <div className="tw-break text-[12px] font-semibold text-foreground">{node.label}</div>
          {node.sub && <div className="tw-break text-muted-foreground">{node.sub}</div>}
          {readiness.data && (
            <Callout tone={readiness.data.ready ? 'info' : 'warning'} className="leading-[1.55]">
              <div className="text-[12px] font-semibold">{readiness.error ? '暂时无法获取最新前提' : readiness.data.ready ? readiness.data.policy === 'verified_only' ? '前提初检通过，执行前还会校验原件' : '当前政策允许派发，线索与证明仍分别记录' : '暂不可执行：前提需要处理'}</div>
              <div className="mt-1 text-[11px] text-muted-foreground">按当前账本核对；图的历史快照与当前断言分别保留。</div>
              <div className="mt-1 space-y-1">
                {readiness.data.premises.length === 0 && <div className="text-[12px]">授权首轮探测，没有声明事实前提。</div>}
                {readiness.data.premises.map((p) => (
                  <div key={p.id} className="text-[12px]">
                    <button className="text-left underline underline-offset-2" onClick={() => onSelectFact(p.id)}>{p.title || p.id}</button>
                    <span> · 断言 v{p.claim_version ?? '?'} · {p.usable ? '准入通过' : p.reason}</span>
                    {p.review_id && <div className="text-muted-foreground">独立核验收据已绑定本版断言；原件完整性另行检查。</div>}
                  </div>
                ))}
              </div>
            </Callout>
          )}
          {readiness.error && <Callout tone="warning" className="leading-[1.55]">暂时无法核对前提，请刷新后再决定是否执行。</Callout>}

          {fact && (
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge size="sm" variant="secondary">
                {fact.confidence}
              </Badge>
              <Badge size="sm" variant="muted">
                proof:{fact.proof ?? 'dynamic'}
              </Badge>
              <span className="ml-auto font-mono text-[11px] text-muted-foreground">
                {fmtTime(fact.created_at)}
              </span>
            </div>
          )}

          {/* Claim identity and review provenance: which assertion version each
              review proved, and whether the assertion changed after the proof.
              It replaces the old note-only review list (a note cannot say which
              version it judged). */}
          {isFact && detail.data && (
            <FactClaimDetail fact={detail.data.fact} reviews={detail.data.reviews} />
          )}

          {fact?.doc && (
            <pre className="tw-mono tw-break max-h-40 overflow-auto whitespace-pre-wrap rounded border border-border bg-muted/40 p-2 text-[11px] leading-[1.55]">
              {fact.doc}
            </pre>
          )}

          {isFact && detail.data && (
            <div>
              <div className="mb-1 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                证据（{detail.data.evidence.length}）
              </div>
              {detail.data.evidence.length === 0 && (
                <div className="text-muted-foreground">没有证据引用</div>
              )}
              {detail.data.evidence.map((ev) => (
                <div
                  key={ev.id}
                  className="mb-0.5 flex items-center gap-1.5 rounded border border-border/60 hover:border-primary/40"
                >
                  <button
                    type="button"
                    onClick={() => setPreview(evidencePreviewFile(ev))}
                    title="点击预览"
                    className="flex min-w-0 flex-1 items-center gap-1.5 py-1 pl-1.5 text-left"
                  >
                    <Eye className="h-2.5 w-2.5 shrink-0 text-muted-foreground" />
                    <span className="tw-break min-w-0 flex-1 font-mono text-[11px]">{ev.uri}</span>
                  </button>
                  <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
                    {fmtBytes(ev.size)}
                  </span>
                  <a
                    href={api.evidenceDownloadURL(ev.id)}
                    download={ev.name || ev.uri.split('/').pop() || undefined}
                    title="下载原文件"
                    aria-label={`下载 ${ev.uri}`}
                    className="shrink-0 px-1.5 py-1 text-muted-foreground hover:text-primary"
                  >
                    <Download className="h-2.5 w-2.5" />
                  </a>
                </div>
              ))}
            </div>
          )}

          {/* 出处：事实意图图记下了从哪来，但打不开（url / artifact / 已删的文件）。
              它不是证据，所以既不做成链接，也不和上面的证据合并计数（D44）。 */}
          {isFact && detail.data?.fact?.refs && detail.data.fact.refs.length > 0 && (
            <div>
              <div className="mb-1 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                出处（打不开，{detail.data.fact.refs.length}）
              </div>
              {detail.data.fact.refs.map((ref, i) => (
                <div
                  key={`${ref.scheme}:${ref.locator}:${i}`}
                  className="mb-0.5 flex items-center gap-1.5 rounded border border-dashed border-border/70 px-1.5 py-1"
                >
                  <span className="shrink-0 rounded bg-muted px-1 font-mono text-[11px] text-muted-foreground">
                    {ref.scheme}
                  </span>
                  <span className="tw-break min-w-0 flex-1 font-mono text-[11px]">{ref.locator}</span>
                </div>
              ))}
              <div className="text-muted-foreground">
                出处只说明"从哪来"，不能当已证实——报告里它只会出现在范围与限制。
              </div>
            </div>
          )}

          {intent && (
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge size="sm" variant="muted">
                尝试 {intent.attempts}
              </Badge>
              {intent.node && (
                <Badge size="sm" variant="info" title="执行节点">
                  {intent.node}
                </Badge>
              )}
              {(intent.profile || node.resolvedProfile) && (
                <Badge
                  size="sm"
                  variant="secondary"
                  title={`请求 profile=${intent.profile || '默认'} · 实际=${node.resolvedProfile || intent.profile || '默认'}`}
                >
                  profile {node.resolvedProfile || intent.profile}
                </Badge>
              )}
              {node.model && (
                <Badge
                  size="sm"
                  variant="outline"
                  className="font-mono"
                  title={`实际使用的模型${node.tier ? `（档位 ${node.tier}）` : ''}`}
                >
                  {node.model}
                </Badge>
              )}
              {intent.parent_intent_id && (
                <Badge size="sm" variant="secondary" title={`父意图 ${intent.parent_intent_id}`}>
                  子任务 ← {intent.parent_intent_id}
                </Badge>
              )}
              <span className="font-mono text-[11px] text-muted-foreground">
                {fmtTime(intent.updated_at)}
              </span>
            </div>
          )}
          {intent?.delegated_by_run_id && (
            <div className="font-mono text-[11px] text-muted-foreground">
              委派 run {intent.delegated_by_run_id}
            </div>
          )}
          {intent?.created_by_participant_id && (
            <div className="text-muted-foreground">
              发起者：{intent.created_by_participant_id}。发起者与父任务分别记录，模型档位不表示上下级。
            </div>
          )}
          {(node.warnings?.length ?? 0) > 0 && (
            <div className="space-y-1 rounded border border-warning/30 bg-warning/10 px-2 py-1.5">
              <div className="text-[11px] font-medium uppercase tracking-wider text-warning">
                ⚠ 告警（{node.warnings!.length}）
              </div>
              {node.warnings!.map((w, i) => (
                <div key={i} className="tw-break text-foreground/80">
                  {/subagent/i.test(w) && (
                    <Badge size="sm" variant="warning" className="mr-1">
                      原生 subagent
                    </Badge>
                  )}
                  {w}
                </div>
              ))}
            </div>
          )}
          {intent?.park_reason && <Callout tone="warning" className="leading-[1.55]">停放理由：{intent.park_reason}</Callout>}
          {intent?.doc && (
            <pre className="tw-mono tw-break max-h-32 overflow-auto whitespace-pre-wrap rounded border border-border bg-muted/40 p-2 text-[11px] leading-[1.55]">
              {intent.doc}
            </pre>
          )}

          {/* Context editing: references/pins/interest entities, version CAS.
              Hard premises stay read-only inside the editor and do not move. */}
          {intent && (
            <IntentContextEditor intent={intent} onSaved={onChanged} onSelectFact={onSelectFact} />
          )}

          {/* Evidence of what this direction's latest executor run actually read,
              frozen at dispatch (Run.premise_claim_versions/context_fact_versions). */}
          {intent && latestRun && (
            <details className="rounded border border-border/60 px-2 py-1.5">
              <summary className="cursor-pointer text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                本次运行读到的版本（{latestRun.id}）
              </summary>
              <div className="mt-1">
                <RunEvidenceView run={latestRun} facts={facts} onSelectFact={onSelectFact} />
              </div>
            </details>
          )}

          {usage.data && (
            <div className="rounded border border-border/60 px-2 py-1.5 text-[11px]">
              <div className="mb-1 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">token 用量</div>
              <div className="flex flex-wrap gap-x-3 gap-y-0.5 font-mono">
                <span>直接 {fmtInt(usage.data.direct.total_tokens)} tok</span>
                <span>子树 {fmtInt(usage.data.subtree.total_tokens)} tok</span>
                <span>{usage.data.subtree.runs} run</span>
                {usage.data.subtree.unknown_usage_runs > 0 && (
                  <span className="text-muted-foreground">
                    {usage.data.subtree.unknown_usage_runs} 个用量未知
                  </span>
                )}
              </div>
            </div>
          )}

          {intent && (
            <div className="rounded border border-border/60 px-2 py-1.5">
              <div className="flex items-center gap-1.5">
                <span className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                  模型接班
                </span>
                <Badge size="sm" variant="muted">交接 v{intent.handoff_version ?? 0}</Badge>
                <Button
                  size="xs"
                  variant="outline"
                  className="ml-auto"
                  onClick={() => setHandoffOpen((v) => !v)}
                >
                  换模型
                </Button>
              </div>
              {handoffOpen && (
                <div className="mt-1.5 space-y-1.5">
                  <Select value={handoffProfile} onValueChange={setHandoffProfile}>
                    <SelectTrigger className="h-7 text-[11px]">
                      <SelectValue placeholder="选择目标模型配置" />
                    </SelectTrigger>
                    <SelectContent>
                      {(profiles.data?.profiles ?? [])
                        .filter((p) => p.runnable)
                        .map((p) => (
                          <SelectItem key={p.id} value={p.id} className="text-[11px]">
                            {p.id} · {p.model}
                          </SelectItem>
                        ))}
                    </SelectContent>
                  </Select>
                  <Input
                    value={handoffNote}
                    onChange={(e) => setHandoffNote(e.target.value)}
                    placeholder="交接说明（已确认事实、做过但无效的尝试、下一步）"
                    className="h-7 text-[12px]"
                  />
                  <div className="flex gap-1.5">
                    <Button size="xs" onClick={() => void doHandoff()} disabled={busy || !handoffProfile}>
                      交接并接班
                    </Button>
                    <Button size="xs" variant="ghost" onClick={() => setHandoffOpen(false)}>
                      取消
                    </Button>
                  </div>
                  <div className="text-muted-foreground">
                    会停掉当前 run、撤销旧身份，用新配置开一个新会话；旧对话不会复制过去。
                  </div>
                </div>
              )}
            </div>
          )}

          {(children.data?.length ?? 0) > 0 && (
            <div>
              <div className="mb-1 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                子任务（{children.data?.length}）
              </div>
              {children.data?.map((child) => (
                <div key={child.id} className="mb-1 rounded border border-border/60 px-2 py-1">
                  <div className="flex items-center gap-1.5">
                    <StatusIndicator
                      compact
                      status={statusKind(child.status)}
                      label={statusLabel(child.status)}
                    />
                    <span className="tw-break min-w-0 flex-1">{child.title}</span>
                    {child.facts > 0 && (
                      <span className="font-mono text-[11px] text-muted-foreground">{child.facts} 事实</span>
                    )}
                  </div>
                  <div className="flex flex-wrap gap-1.5 font-mono text-[11px] text-muted-foreground">
                    <span>{child.id}</span>
                    {child.profile ? <span>· {child.profile}</span> : null}
                    {child.stop_reason ? <span>· {child.stop_reason}</span> : null}
                  </div>
                </div>
              ))}
            </div>
          )}

          {/* 重派/续话备注：给操作员和下一轮决策看的历史，不是执行者约束。
              它不在 intent.doc 里，派发提示词也不读（D72）。 */}
          {intent?.notes && intent.notes.length > 0 && (
            <div>
              <div className="mb-1 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                <MessageSquare className="h-3 w-3" /> 重派/续话备注（{intent.notes.length}）
              </div>
              {intent.notes.map((n, i) => (
                <div key={`${n.at}:${i}`} className="mb-1 rounded border border-border/60 px-2 py-1">
                  <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                    <Badge size="sm" variant={n.kind === 'retry' ? 'warning' : 'info'}>
                      {n.kind === 'retry' ? '重派' : n.kind === 'steer' ? '续话' : n.kind}
                    </Badge>
                    <span className="font-mono">{fmtTime(n.at)}</span>
                    {n.ref_fact && <span className="ml-auto truncate font-mono">依据 {n.ref_fact}</span>}
                  </div>
                  <div className="tw-break mt-0.5 text-foreground/85">{n.text}</div>
                </div>
              ))}
              <div className="text-muted-foreground">
                这是操作员/指挥官给本轮的解释，不会进入执行者的约束段。
              </div>
            </div>
          )}

          {intent?.interjections && intent.interjections.length > 0 && (
            <div>
              <div className="mb-1 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                <Send className="h-3 w-3" /> 插话记录（{intent.interjections.length}）
              </div>
              {intent.interjections.map((it, i) => (
                <div key={`${it.at}:${i}`} className="mb-1 rounded border border-border/60 px-2 py-1">
                  <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                    <span className="font-mono">{fmtTime(it.at)}</span>
                    {it.interrupt && (
                      <Badge size="sm" variant="warning">
                        打断
                      </Badge>
                    )}
                    {it.message_id && <span className="ml-auto truncate font-mono">{it.message_id}</span>}
                  </div>
                  <div className="tw-break mt-0.5 text-foreground/85">{it.text}</div>
                </div>
              ))}
            </div>
          )}

          {goal && (
            <div className="rounded border border-border/60 px-2 py-1.5">
              <div className="text-muted-foreground">
                {goal.quantified_target || '没有量化条件'}
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px]">
                <Badge size="sm" variant={goal.met ? 'success' : 'warning'}>
                  {goal.met ? '已达成' : '未达成'}
                </Badge>
                <span className="text-muted-foreground">
                  {goal.completed_at
                    ? `达成于 ${fmtTime(goal.completed_at)}`
                    : `创建于 ${fmtTime(goal.created_at)}`}
                </span>
              </div>
              {goal.met && (
                <div className="mt-1.5 border-t border-border/60 pt-1.5">
                  <div className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                    为什么达成
                  </div>
                  {goal.note ? (
                    <div className="tw-break mt-0.5 text-foreground/85">
                      {goal.note}
                    </div>
                  ) : (
                    <div className="mt-0.5 italic text-muted-foreground">
                      达成理由未记录（旧数据）
                    </div>
                  )}
                  <div className="mt-1 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                    依据{goalRefs.legacy ? '（证据所在结论）' : ''}
                  </div>
                  {goalRefs.items.length > 0 ? (
                    <div className="mt-0.5 space-y-0.5">
                      {goalRefs.items.map(({ id, fact: refFact }) => (
                        <button
                          key={id}
                          type="button"
                          onClick={() => onSelectFact(refFact?.id ?? id)}
                          className="flex w-full items-center gap-1.5 rounded px-1 py-0.5 text-left hover:bg-muted/60"
                          title={refFact ? `${id} · ${refFact.title}` : `${id}（事实不在当前列表）`}
                        >
                          <span className="shrink-0 font-mono text-[11px] text-info">{id}</span>
                          <span className="min-w-0 flex-1 truncate text-foreground/85">
                            {refFact?.title ?? '事实不在当前列表'}
                          </span>
                        </button>
                      ))}
                    </div>
                  ) : (
                    <div className="mt-0.5 font-mono text-[11px] text-muted-foreground">
                      {goal.evidence_id ? `证据 ${goal.evidence_id}` : '未记录依据'}
                    </div>
                  )}
                  {/* The receipt is a historical record: a later correction of a
                      cited fact does not rewrite it. The server computes this
                      flag; saying it is stale is the honest report, un-setting
                      `met` would erase the proof the operator already accepted. */}
                  {goal.receipt_stale && (
                    <Callout tone="warning" className="mt-1.5 leading-[1.55]">
                      <div className="text-[12px] font-semibold">
                        收据过时：所依据事实已更新，需重新判断（历史完成证明保留）
                      </div>
                      {(goal.receipt_changed ?? []).length > 0 && (
                        <div className="mt-1 flex flex-wrap items-center gap-1 text-[11px]">
                          <span className="text-muted-foreground">变更事实</span>
                          {goal.receipt_changed!.map((fid) => (
                            <button
                              key={fid}
                              type="button"
                              onClick={() => onSelectFact(fid)}
                              className="rounded border border-warning/40 bg-warning/10 px-1 font-mono text-[11px] leading-4 text-warning hover:border-warning/70 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                              title={`在图谱中打开事实 ${fid}`}
                            >
                              {fid}
                            </button>
                          ))}
                        </div>
                      )}
                      <div className="mt-1 text-muted-foreground">
                        目标仍记为已达成；历史盖章不会被自动撤销。
                      </div>
                    </Callout>
                  )}
                </div>
              )}
            </div>
          )}

          {/* actions */}
          <div className={`flex flex-wrap gap-1.5 ${intent || fact ? 'border-t border-border/50 pt-3' : ''}`}>
            {/* A done direction cannot be claimed for a fresh run; "续跑" below
                continues its session instead. Showing 派发 on done only produced
                "无法认领（状态为 done）". */}
            {intent && intent.status === 'pending' && (
              <Button size="xs" variant="soft" disabled={busy} onClick={() => run(() => api.runIntent(intent.id), '已派发')}>
                <Play className="h-3 w-3" /> 派发
              </Button>
            )}
            {intent &&
              (intent.status === 'pending' || intent.status === 'running' || intent.status === 'claimed') && (
                <Button
                  size="xs"
                  variant="soft"
                  disabled={busy}
                  onClick={() => onChildIntent({ id: intent.id, title: intent.title })}
                  title="把一块工作挂到当前方向下面（可选）：子任务有自己的 run、会自动开跑，父方向继续"
                >
                  <GitBranch className="h-3 w-3" /> 派子意图
                </Button>
              )}
            {intent && intent.status === 'running' && intent.run_id && (
              <Button size="xs" variant="ghost" disabled={busy} onClick={() => run(() => api.cancelRun(intent.run_id!), '已取消')}>
                <Activity className="h-3 w-3" /> 取消
              </Button>
            )}
            {intent && intent.status === 'running' && (
              <Button size="xs" variant="soft" disabled={busy} onClick={openIntervene}>
                <Send className="h-3 w-3" /> 干预
              </Button>
            )}
            {intent && (intent.status === 'failed' || intent.status === 'parked') && (
              <Button size="xs" variant="ghost" disabled={busy} onClick={() => run(() => api.reopenIntent(intent.id), '已重开')}>
                <RotateCcw className="h-3 w-3" /> 重开
              </Button>
            )}
            {intent && (intent.status === 'failed' || intent.status === 'pending') && (
              <Button
                size="xs"
                variant="outline"
                disabled={busy}
                onClick={() => setRetrying(true)}
                title="重试：可续跑原会话（保留记忆）或重派新会话（丢弃记忆），都会重投未确认消息"
              >
                <RefreshCw className="h-3 w-3" /> 重试
              </Button>
            )}
            {intent && (intent.status === 'done' || intent.status === 'parked') && (
              <Button
                size="xs"
                variant="outline"
                disabled={busy}
                onClick={() =>
                  run(
                    () => api.continueIntent(intent.id),
                    '已续跑：同一会话继续（未重发任务书）',
                  )
                }
                title="同一会话继续，不重发任务书；没有原会话会报 continue_unavailable，用重试新会话"
              >
                <RefreshCw className="h-3 w-3" /> 续跑
              </Button>
            )}
            {intent && intent.status !== 'parked' && (
              <Button size="xs" variant="ghost" onClick={() => setParking((v) => !v)}>
                停放
              </Button>
            )}
            {fact && (fact.kind === 'vuln' || fact.kind === 'cred' || (premisePolicy.data?.effective === 'verified_only' && ['host','service','loot','artifact'].includes(fact.kind))) && fact.status === 'unverified' && (
              <Button size="xs" variant="soft" disabled={busy} onClick={() => run(() => api.verifyFact(fact.id), '已排入复核')}>
                <ShieldCheck className="h-3 w-3" /> 复核
              </Button>
            )}
            {/* A human may only take a conclusion down or put it back up: 证实
                is a verifier rerun, never a button (D41). */}
            {fact && (fact.kind === 'vuln' || fact.kind === 'cred' || (premisePolicy.data?.effective === 'verified_only' && ['host','service','loot','artifact'].includes(fact.kind))) && fact.status !== 'rejected' && (
              <Button size="xs" variant="ghost" disabled={busy} onClick={() => setRejecting((v) => !v)}>
                <ShieldX className="h-3 w-3" /> 判定不成立
              </Button>
            )}
            {fact && fact.status === 'rejected' && (
              <Button
                size="xs"
                variant="ghost"
                disabled={busy}
                onClick={() =>
                  run(() => api.reviewFact(fact.id, 'needs_evidence', '人工撤销驳回：回到未验证'), '已撤销驳回')
                }
              >
                <RotateCcw className="h-3 w-3" /> 撤销驳回
              </Button>
            )}
          </div>

          {intervening && intent && intent.status === 'running' && (
            <div className="space-y-1.5 rounded border border-primary/25 bg-primary/5 p-2">
              <div className="flex gap-1">
                <Button
                  size="xs"
                  variant={interveneMode === 'say' ? 'soft' : 'ghost'}
                  onClick={() => setInterveneMode('say')}
                >
                  插话
                </Button>
                <Button
                  size="xs"
                  variant={interveneMode === 'steer' ? 'soft' : 'ghost'}
                  onClick={() => setInterveneMode('steer')}
                >
                  中断续话
                </Button>
              </div>
              <div className="text-muted-foreground">
                {interveneMode === 'say'
                  ? '进入当前 run，不取消、不重开；默认在下一轮模型调用前生效。不设硬上限，但每条会多一轮模型步。'
                  : '取消当前 run，用同一轨迹开一个新 turn；已写出的证据保留。steer 每方向最多 3 次。'}
              </div>
              <Input
                value={interveneText}
                onChange={(e) => setInterveneText(e.target.value)}
                placeholder={
                  interveneMode === 'say'
                    ? '例如：停止铺路径字典，收窄到登录口'
                    : '例如：先停，换到 /portal/login 登录口做单变量差分'
                }
                className="h-7 text-[12px]"
                maxLength={500}
              />
              <Input
                value={refFact}
                onChange={(e) => setRefFact(e.target.value)}
                placeholder="依据事实 id（可选，如 f_xxx）"
                className="h-7 font-mono text-[11px]"
              />
              {interveneMode === 'say' && (
                <label className="flex items-center gap-1.5 text-muted-foreground">
                  <input
                    type="checkbox"
                    checked={interrupt}
                    onChange={(e) => setInterrupt(e.target.checked)}
                  />
                  打断当前模型请求（只跳过尚未开始的工具）
                </label>
              )}
              <div className="flex items-center gap-1.5">
                <Button size="xs" disabled={busy || !interveneText.trim()} onClick={sendIntervene}>
                  {interveneMode === 'say' ? '发送到当前 run' : '中断并续话'}
                </Button>
                <span className="text-[11px] text-muted-foreground">
                  {interveneMode === 'say'
                    ? `${intent.say_count ?? 0} 次（不限）`
                    : `${intent.steer_count ?? 0}/3`}
                </span>
              </div>
            </div>
          )}

          {rejecting && fact && (
            <div className="space-y-1">
              <Input
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                placeholder="为什么这条不成立（必填）"
                className="h-7 text-[12px]"
              />
              <Button
                size="xs"
                disabled={busy || !reason.trim()}
                onClick={() =>
                  run(async () => {
                    await api.reviewFact(fact.id, 'reject', reason.trim())
                    setRejecting(false)
                    setReason('')
                  }, '已判定不成立')
                }
              >
                确认判定
              </Button>
            </div>
          )}

          {parking && intent && (
            <div className="space-y-1">
              <Input
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                placeholder="停放理由（必填）"
                className="h-7 text-[12px]"
              />
              <Button
                size="xs"
                disabled={busy || !reason.trim()}
                onClick={() => run(() => api.parkIntent(intent.id, reason.trim()), '已停放')}
              >
                确认停放
              </Button>
            </div>
          )}

          {note && <div className="text-muted-foreground">{note}</div>}
        </div>
      </ScrollArea>

      {intent && (
        <RetryIntentDialog
          open={retrying}
          onOpenChange={setRetrying}
          intentId={intent.id}
          title={intent.title}
          onRetried={(r) => {
            setNote(
              `已重试（${r.resume ? '续跑原会话' : '重派新会话'}）：新 run ${r.run_id}，重投 ${r.redelivered} 条未确认消息。`,
            )
            onChanged()
          }}
        />
      )}

      <EvidencePreview file={preview} onOpenChange={(open) => !open && setPreview(null)} />
    </div>
  )
}

function isnStatus(s: string): boolean {
  return ['pending', 'claimed', 'running', 'done', 'failed', 'parked', 'unverified', 'verified', 'rejected'].includes(s)
}

export default GraphNodePanel
