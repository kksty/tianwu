import type { ReactNode } from 'react'
import { Badge } from '@cyber/ui'
import { DOCK_DEFAULT_WIDTH } from '@/components/graph/dock-width'
import { intentStatusDot } from '@/graph/status'
import { edgeKindLabel, type GraphEdge, type GraphNode } from '@/graph/model'
import { fmtInt, fmtTime, kindVariant, statusKind, statusLabel } from '@/lib/format'
import type { RunPulse } from '@/lib/types'

function statusText(status?: string): string {
  if (!status) return ''
  if (status === 'met') return '已达成'
  if (status === 'open') return '未达成'
  return statusLabel(status)
}

/** Why a boundary leaf is not a real node (internal/retrieval/graph_page.go). */
function boundaryText(state?: string): string {
  switch (state) {
    case 'not_loaded':
      return '当前快照没有载入这条引用指向的记录（账本里仍然存在）。它不是孤立事实，也不代表数据缺失：在图谱里点这个小节点，会沿引用加载它的邻域。'
    case 'missing':
      return '账本里也没有这个 id：引用已经缺失，点开不会变出内容。'
    case 'rejected':
      return '这条引用指向的事实已被明确否定：记录还在账本里，但不应作为当前依据。'
    default:
      return '尚未载入的引用：点开可以沿它加载邻域。'
  }
}

/** The short title of a boundary leaf, so the card never reads like a fact row. */
function boundaryTitle(state?: string): string {
  switch (state) {
    case 'not_loaded':
      return '未载入的引用'
    case 'missing':
      return '引用缺失'
    case 'rejected':
      return '已否定的引用'
    default:
      return '引用边界'
  }
}

function boundaryBadge(state?: string): { label: string; variant: 'info' | 'warning' | 'muted' } {
  switch (state) {
    case 'not_loaded':
      return { label: '未载入', variant: 'info' }
    case 'missing':
      return { label: '缺失', variant: 'warning' }
    case 'rejected':
      return { label: '已否定', variant: 'warning' }
    default:
      return { label: '引用', variant: 'muted' }
  }
}

function badgeVariant(kind: string, status?: string) {
  if (kind === 'intent') return 'primary'
  if (kind === 'goal') return status === 'met' ? 'success' : 'warning'
  if (kind === 'project') return 'secondary'
  return kindVariant(kind)
}

/** The badge for a relation edge: premise / reference / pin / supports are
 *  different relations, not shades of the same "intent" line. */
function edgeBadge(kind: GraphEdge['kind']): { label: string; variant: 'primary' | 'secondary' | 'info' | 'warning' | 'success' | 'muted' } {
  switch (kind) {
    case 'premise':
      return { label: edgeKindLabel(kind), variant: 'info' }
    case 'reference':
      return { label: edgeKindLabel(kind), variant: 'secondary' }
    // A pinned read is still context, never a gate; it is called out separately
    // because the canvas draws it as a green double line, not a dashed one.
    case 'pin':
      return { label: edgeKindLabel(kind), variant: 'info' }
    case 'supports':
      return { label: edgeKindLabel(kind), variant: 'warning' }
    case 'spawn':
      return { label: edgeKindLabel(kind), variant: 'secondary' }
    case 'brief':
      return { label: edgeKindLabel(kind), variant: 'muted' }
    case 'host':
      return { label: edgeKindLabel(kind), variant: 'muted' }
    case 'goal':
      return { label: edgeKindLabel(kind), variant: 'success' }
    default:
      // The produced-fact fan has no relation name; it is the direction's output.
      return { label: edgeKindLabel(kind) || 'intent', variant: 'primary' }
  }
}

/** A quiet property row: 11px label in a fixed 56px column, 13px value. */
function Meta({
  label,
  value,
  className,
  title,
  mono = true,
}: {
  label: string
  value: string
  className?: string
  title?: string
  /** False for prose (a goal's reason), where monospace hurts the reading. */
  mono?: boolean
}) {
  return (
    <div className={`flex items-baseline gap-2 py-0.5 ${className ?? ''}`} title={title}>
      <span className="w-14 shrink-0 text-[11px] leading-4 text-ink-3">{label}</span>
      <span
        className={`tw-break min-w-0 flex-1 text-[13px] leading-5 text-ink-2 ${
          mono ? 'font-mono tw-nums' : ''
        }`}
      >
        {value}
      </span>
    </div>
  )
}

/** One titled block inside the card: eyebrow + hairline, matching NodePanel. */
function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="border-t border-hairline pt-2 first:border-t-0 first:pt-0">
      <div className="tw-eyebrow mb-1">{title}</div>
      <div className="space-y-0.5">{children}</div>
    </div>
  )
}

/**
 * The hover/pin inspector. It shows what the pointer is on (a node, a ghost
 * intent, or an intent edge) in the top-right corner, so the graph does not
 * need to be clicked to be read. Clicking still pins the full NodePanel, which
 * carries the actions.
 */
export function GraphInspector({
  node,
  edge,
  pulse,
  pinned = false,
  model,
  width = DOCK_DEFAULT_WIDTH,
}: {
  node?: GraphNode | null
  edge?: GraphEdge | null
  pulse?: RunPulse
  pinned?: boolean
  /** Model resolved from the step node when only an edge is hovered. */
  model?: string
  /** Shared width of the graph's right-hand overlays (see dock-width.ts). */
  width?: number
}) {
  const fact = node?.meta?.fact
  const goal = node?.meta?.goal
  const project = node?.meta?.project
  const intent = node?.meta?.intent ?? edge?.intent
  const edgeInfo = edge ? edgeBadge(edge.kind) : null
  const status = node?.status ?? edge?.status ?? intent?.status
  // Intent statuses use the themed graph status vocabulary (running red,
  // reviewing teal, pending amber, …); a fact's verified/unverified status
  // keeps the shared success/error/idle chip palette.
  const intentDot = status ? intentStatusDot(status) : undefined
  // A boundary leaf is a reference marker, not a fact/intent row: it gets its
  // own title, badge and explanation instead of the generic node metadata.
  const isBoundary = node?.type === 'boundary'
  const boundaryInfo = isBoundary ? boundaryBadge(node.boundaryState) : null
  // An intent node/edge is the direction; a relation edge names the relation.
  const kind = fact?.kind ?? (intent || edge ? 'intent' : (node?.type ?? ''))
  const badge = boundaryInfo
    ? { label: '引用边界', variant: 'muted' as const }
    : edgeInfo && !node
      ? edgeInfo
      : { label: kind, variant: badgeVariant(kind, status) }
  const title = isBoundary
    ? boundaryTitle(node.boundaryState)
    : node?.label || intent?.title || edge?.label || edgeInfo?.label || ''
  const summary = isBoundary ? '' : node?.sub || intent?.description || ''
  const hasDetails = Boolean(fact || intent || goal || project || pulse)
  const warnings = node?.warnings ?? []
  // fact_meta 在 projection=graph 下仍带着复核状态与数量（evidence_ids/refs 被清空）：
  // 节点字段优先，原文数组只在整页详情可用时兜底。
  const needsReview = Boolean(node?.needsReview)
  const reviewedClaimVersion = node?.reviewedClaimVersion ?? 0
  const currentClaimVersion = fact?.claim_version ?? 0
  const evidenceCount = node?.evidenceCount ?? fact?.evidence_ids?.length ?? 0
  const refCount = node?.refCount ?? fact?.refs?.length ?? 0
  const profile =
    intent?.profile || node?.resolvedProfile || (edge?.kind === 'spawn' ? intent?.profile : '') || ''
  const resolvedModel = (node?.model ?? model) as string | undefined

  return (
    // The card is a hover overlay for the canvas (pointer-events-none), so its
    // body cannot be scrolled by the wheel. It must therefore grow with its
    // content: a max-height clamp here would silently cut off the warning and
    // receipt rows on short viewports.
    <div
      className="pointer-events-none absolute right-3 top-[56px] flex max-w-[calc(100vw-1.5rem)] flex-col overflow-hidden rounded-xl border border-hairline bg-card/95 shadow-elevated backdrop-blur"
      style={{ width }}
    >
      <div className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-hairline bg-surface-1/60 px-3 py-2">
        <Badge size="sm" variant={badge.variant}>
          {badge.label}
        </Badge>
        {boundaryInfo && (
          <Badge size="sm" variant={boundaryInfo.variant}>
            {boundaryInfo.label}
          </Badge>
        )}
        {status && statusText(status) && (
          <span className="inline-flex items-center gap-1 text-[11px] text-ink-3">
            <span
              className={`h-1.5 w-1.5 shrink-0 rounded-full ${
                intentDot
                  ? `${intentDot} ${status === 'running' || status === 'reviewing' ? 'animate-pulse' : ''}`
                  : statusKind(status) === 'success'
                    ? 'bg-success'
                    : statusKind(status) === 'error'
                      ? 'bg-destructive'
                      : statusKind(status) === 'running'
                        ? 'animate-pulse bg-info'
                        : statusKind(status) === 'warning'
                          ? 'bg-warning'
                          : 'bg-ink-3'
              }`}
            />
            {statusText(status)}
          </span>
        )}
        {warnings.length > 0 && (
          <Badge size="sm" variant="warning" title={warnings.join('\n')}>
            ⚠ {warnings.length}
          </Badge>
        )}
        {needsReview && (
          <Badge
            size="sm"
            variant="warning"
            title={`当前断言 v${currentClaimVersion}，最新复核只到 v${reviewedClaimVersion}（0 = 从未复核）：复核的是旧断言`}
          >
            复核的是旧断言
          </Badge>
        )}
        <span className="ml-auto font-mono text-[11px] text-ink-3">
          {isBoundary
            ? '引用'
            : node?.type === 'ghost'
              ? 'ghost intent'
              : edgeInfo
                ? edgeInfo.label
                : (node?.type ?? 'intent')}
        </span>
      </div>

      <div className="min-h-0 flex-1 space-y-2.5 overflow-y-auto px-3 py-2.5">
        <div className="tw-break text-[13px] font-semibold leading-5 text-ink">{title || '（无标题）'}</div>
        {summary && (
          /* Hovering is a request to read the node: up to eight lines of summary
             instead of a three-line tease. Clamped so the card height stays
             bounded (the card is pointer-transparent and cannot be scrolled). */
          <div className="tw-break line-clamp-[8] text-[12.5px] leading-relaxed text-ink-2" title={summary}>
            {summary}
          </div>
        )}
        {isBoundary && (
          <>
            <div className="tw-break rounded-lg border border-hairline bg-surface-2/60 px-2.5 py-2 text-[12px] leading-relaxed text-ink-2">
              {boundaryText(node?.boundaryState)}
            </div>
            <div className="flex items-baseline gap-2">
              <span className="w-14 shrink-0 text-[11px] leading-4 text-ink-3">引用 id</span>
              <span className="tw-break min-w-0 flex-1 font-mono text-[13px] leading-5 text-ink-2">
                {node?.boundary}
              </span>
            </div>
            {node?.boundaryState === 'not_loaded' && (
              <div className="text-[12px] leading-relaxed text-ink-3">
                在图谱里点击这个边界节点，会沿它加载邻域；它不会像事实节点那样固定详情。
              </div>
            )}
          </>
        )}

        {hasDetails && (
          <div className="space-y-2.5">
            {fact && (
              <Section title="事实">
                <Meta label="置信" value={fact.confidence || '—'} />
                <Meta label="证明" value={fact.proof ?? 'dynamic'} />
                <Meta label="证据" value={`${fmtInt(evidenceCount)}`} />
                <Meta label="引用" value={`${fmtInt(refCount)}`} />
                <Meta label="创建" value={fmtTime(fact.created_at)} />
                {needsReview && (
                  <Meta
                    label="复核"
                    value={`旧断言 · 复核 v${reviewedClaimVersion}（当前 v${currentClaimVersion}）`}
                    className="text-warning"
                    title="claim_version 高于最新复核版本：这次复核证明的不是当前断言"
                  />
                )}
              </Section>
            )}
            {intent && (
              <Section title="意图">
                <Meta label="尝试" value={fmtInt(intent.attempts)} />
                <Meta label="节点" value={intent.node || '默认'} />
                <Meta label="profile" value={profile || '默认'} />
                {/* The model is what the operator actually wants to know; the tier
                    is only a routing label, so it rides in the tooltip. */}
                {resolvedModel ? (
                  <Meta
                    label="模型"
                    value={resolvedModel}
                    title={node?.tier ? `档位 ${node.tier}` : undefined}
                  />
                ) : null}
                {node?.resolvedProfile && node.resolvedProfile !== profile && (
                  <Meta label="实际 profile" value={node.resolvedProfile} />
                )}
                {intent.parent_intent_id && <Meta label="父意图" value={intent.parent_intent_id} />}
                {intent.delegated_by_run_id && (
                  <Meta label="委派 run" value={intent.delegated_by_run_id} />
                )}
                {edge?.kind === 'spawn' && edge.parentIntent && (
                  <Meta label="血缘" value={`${edge.parentIntent} → ${intent.id}`} />
                )}
                {intent.run_id && <Meta label="run" value={intent.run_id} />}
                {intent.updated_at && <Meta label="更新" value={fmtTime(intent.updated_at)} />}
                {intent.steer_count ? <Meta label="续话" value={`${intent.steer_count}`} /> : null}
                {intent.say_count ? <Meta label="插话" value={`${intent.say_count}`} /> : null}
                {intent.notes?.length ? <Meta label="备注" value={`${intent.notes.length}`} /> : null}
                {intent.park_reason && <Meta label="停放" value={intent.park_reason} mono={false} />}
              </Section>
            )}
            {pulse && (
              <Section title="运行">
                <Meta label="静默" value={`${fmtInt(pulse.idle_seconds)}s`} />
                <Meta label="事件" value={fmtInt(pulse.events)} />
                {pulse.context_tokens ? (
                  <Meta
                    label="上下文"
                    value={`${fmtInt(pulse.context_tokens)} / ${fmtInt(pulse.context_window ?? 0)}`}
                  />
                ) : null}
                {pulse.state && <Meta label="状态" value={pulse.state} />}
              </Section>
            )}
            {goal && (
              <Section title="目标">
                <Meta label="目标" value={goal.met ? '已达成' : '未达成'} />
                {goal.start && <Meta label="起点" value={goal.start} mono={false} />}
                {/* Goals written by older consoles carry a quantified criterion
                    instead of a start; show it as history rather than hide it. */}
                {!goal.start && goal.quantified_target && (
                  <Meta label="口径（旧记录）" value={goal.quantified_target} mono={false} />
                )}
                {goal.note && (
                  <Meta
                    label="理由"
                    value={goal.note}
                    className="line-clamp-2"
                    title={goal.note}
                    mono={false}
                  />
                )}
                {goal.completed_at && <Meta label="达成" value={fmtTime(goal.completed_at)} />}
                {/* The seal is a historical receipt: when a cited fact changed, the
                    server says so (computed, never rewritten). Never unset met here. */}
                {goal.receipt_stale && (
                  <Meta
                    label="收据"
                    value={`过时 · 所依据事实已更新${goal.receipt_changed?.length ? `：${goal.receipt_changed.join('、')}` : ''}`}
                    className="text-warning"
                    title="完成证明保留：目标仍记为已达成，但依据的断言已变，需要重新判断"
                  />
                )}
              </Section>
            )}
            {project && (
              <Section title="项目">
                {project.scope && <Meta label="授权边界" value={project.scope} mono={false} />}
                <Meta label="意图" value={fmtInt(project.intents)} />
                <Meta label="事实" value={fmtInt(project.facts)} />
              </Section>
            )}
          </div>
        )}

        {!hasDetails && !isBoundary && (
          <div className="rounded-lg border border-dashed border-hairline px-2.5 py-2 text-[12px] leading-relaxed text-ink-3">
            悬停只显示关键属性；单击节点固定完整详情面板。
          </div>
        )}

        {warnings.length > 0 && (
          <div className="space-y-1 rounded-lg border border-warning/25 bg-warning/[0.07] px-2.5 py-2">
            <div className="text-[11px] font-semibold text-warning">⚠ 告警（{warnings.length}）</div>
            {warnings.slice(0, 3).map((w, i) => {
              const native = /subagent/i.test(w)
              return (
                <div key={i} className="tw-break text-[12px] leading-relaxed text-ink-2">
                  {native && (
                    <Badge size="sm" variant="warning" className="mr-1">
                      原生 subagent
                    </Badge>
                  )}
                  {w}
                </div>
              )
            })}
            {warnings.length > 3 && (
              <div className="text-[11px] text-ink-3">…还有 {warnings.length - 3} 条，见运行页</div>
            )}
          </div>
        )}
      </div>

      <div className="shrink-0 border-t border-hairline px-3 py-1.5 text-[11px] text-ink-3">
        {isBoundary
          ? node?.boundaryState === 'not_loaded'
            ? '点开边界节点加载邻域 · 它不是孤立事实'
            : '引用边界 · 它不是孤立事实'
          : pinned
            ? '已固定 · 点击空白处取消'
            : '单击节点固定详情 · 移开鼠标即隐藏'}
      </div>
    </div>
  )
}
