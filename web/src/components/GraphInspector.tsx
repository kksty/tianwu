import { Badge } from '@cyber/ui'
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
      return '此页快照未载入这条引用：点开沿它加载邻域，不代表它缺失。'
    case 'missing':
      return '账本里也没有这个 id：引用缺失，展开不会变出内容。'
    case 'rejected':
      return '引用的事实已被明确否定：仍存在于账本，但不应作为当前依据。'
    default:
      return '尚未载入的引用：点开沿它加载邻域。'
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
    // because the canvas draws it as a teal double line, not a dashed one.
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
}: {
  node?: GraphNode | null
  edge?: GraphEdge | null
  pulse?: RunPulse
  pinned?: boolean
  /** Model resolved from the step node when only an edge is hovered. */
  model?: string
}) {
  const fact = node?.meta?.fact
  const goal = node?.meta?.goal
  const project = node?.meta?.project
  const intent = node?.meta?.intent ?? edge?.intent
  const edgeInfo = edge ? edgeBadge(edge.kind) : null
  const status = node?.status ?? edge?.status ?? intent?.status
  // An intent node/edge is the direction; a relation edge names the relation.
  const kind = fact?.kind ?? (intent || edge ? 'intent' : (node?.type ?? ''))
  const badge = edgeInfo && !node ? edgeInfo : { label: kind, variant: badgeVariant(kind, status) }
  const title = node?.label || intent?.title || edge?.label || edgeInfo?.label || ''
  const summary = node?.sub || intent?.description || ''
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

  return (
    <div className="pointer-events-none absolute right-3 top-[56px] w-[320px] overflow-hidden rounded-lg border border-border bg-card/95 shadow-elevated backdrop-blur">
      <div className="flex items-center gap-2 border-b border-border px-3 py-2">
        <Badge size="sm" variant={badge.variant}>
          {badge.label}
        </Badge>
        {status && statusText(status) && (
          <span className="inline-flex items-center gap-1 text-[11px] text-muted-foreground">
            <span
              className={`h-1.5 w-1.5 rounded-full ${
                statusKind(status) === 'success'
                  ? 'bg-success'
                  : statusKind(status) === 'error'
                    ? 'bg-destructive'
                    : statusKind(status) === 'running'
                      ? 'animate-pulse bg-info'
                      : statusKind(status) === 'warning'
                        ? 'bg-warning'
                        : 'bg-muted-foreground'
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
        <span className="ml-auto font-mono text-[10px] text-muted-foreground">
          {node?.type === 'ghost' ? 'ghost intent' : edgeInfo ? edgeInfo.label : (node?.type ?? 'intent')}
        </span>
      </div>

      <div className="space-y-2 px-3 py-2">
        <div className="tw-break text-[13px] font-medium leading-snug text-foreground">{title || '（无标题）'}</div>
        {summary && (
          <div className="tw-break line-clamp-3 text-[11px] leading-relaxed text-muted-foreground" title={summary}>
            {summary}
          </div>
        )}
        {node?.type === 'boundary' && (
          <div className="rounded border border-border/60 bg-muted/30 px-1.5 py-1 text-[10.5px] leading-relaxed text-muted-foreground">
            {boundaryText(node.boundaryState)}
          </div>
        )}

        <div className="grid grid-cols-2 gap-x-3 gap-y-1 border-t border-border/60 pt-1.5 text-[11px]">
          {fact && (
            <>
              <Meta label="置信" value={fact.confidence || '—'} />
              <Meta label="证明" value={fact.proof ?? 'dynamic'} />
              <Meta label="证据" value={`${evidenceCount}`} />
              <Meta label="引用" value={`${refCount}`} />
              <Meta label="创建" value={fmtTime(fact.created_at)} />
              {needsReview && (
                <Meta
                  label="复核"
                  value={`旧断言 · 复核 v${reviewedClaimVersion}（当前 v${currentClaimVersion}）`}
                  className="col-span-2 text-warning"
                  title="claim_version 高于最新复核版本：这次复核证明的不是当前断言"
                />
              )}
            </>
          )}
          {intent && (
            <>
              <Meta label="尝试" value={fmtInt(intent.attempts)} />
              <Meta label="节点" value={intent.node || '默认'} />
              <Meta label="profile" value={profile || '默认'} />
              {/* The model is what the operator actually wants to know; the tier
                  is only a routing label, so it rides in the tooltip. */}
              {node?.model || model ? (
                <Meta
                  label="模型"
                  value={(node?.model ?? model) as string}
                  className="col-span-2"
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
                <Meta label="血缘" value={`${edge.parentIntent} → ${intent.id}`} className="col-span-2" />
              )}
              {intent.run_id && <Meta label="run" value={intent.run_id} />}
              {intent.updated_at && <Meta label="更新" value={fmtTime(intent.updated_at)} />}
              {intent.steer_count ? <Meta label="续话" value={`${intent.steer_count}`} /> : null}
              {intent.say_count ? <Meta label="插话" value={`${intent.say_count}`} /> : null}
              {intent.notes?.length ? <Meta label="备注" value={`${intent.notes.length}`} /> : null}
              {intent.park_reason && <Meta label="停放" value={intent.park_reason} className="col-span-2" />}
            </>
          )}
          {pulse && (
            <>
              <Meta label="静默" value={`${fmtInt(pulse.idle_seconds)}s`} />
              <Meta label="事件" value={fmtInt(pulse.events)} />
              {pulse.context_tokens ? (
                <Meta
                  label="上下文"
                  value={`${fmtInt(pulse.context_tokens)} / ${fmtInt(pulse.context_window ?? 0)}`}
                  className="col-span-2"
                />
              ) : null}
              {pulse.state && <Meta label="状态" value={pulse.state} className="col-span-2" />}
            </>
          )}
          {goal && (
            <>
              <Meta label="目标" value={goal.met ? '已达成' : '未达成'} />
              {goal.quantified_target && <Meta label="量化" value={goal.quantified_target} className="col-span-2" />}
              {goal.note && (
                <Meta
                  label="理由"
                  value={goal.note}
                  className="col-span-2 line-clamp-2"
                  title={goal.note}
                  mono={false}
                />
              )}
              {goal.completed_at && (
                <Meta label="达成" value={fmtTime(goal.completed_at)} className="col-span-2" />
              )}
              {/* The seal is a historical receipt: when a cited fact changed, the
                  server says so (computed, never rewritten). Never unset met here. */}
              {goal.receipt_stale && (
                <Meta
                  label="收据"
                  value={`过时 · 所依据事实已更新${goal.receipt_changed?.length ? `：${goal.receipt_changed.join('、')}` : ''}`}
                  className="col-span-2 text-warning"
                  title="完成证明保留：目标仍记为已达成，但依据的断言已变，需要重新判断"
                />
              )}
            </>
          )}
          {project && (
            <>
              {project.scope && <Meta label="授权边界" value={project.scope} className="col-span-2" />}
              <Meta label="意图" value={fmtInt(project.intents)} />
              <Meta label="事实" value={fmtInt(project.facts)} />
            </>
          )}
        </div>

        {warnings.length > 0 && (
          <div className="space-y-1 rounded border border-warning/30 bg-warning/10 px-2 py-1.5">
            <div className="flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wider text-warning">
              ⚠ 告警（{warnings.length}）
            </div>
            {warnings.slice(0, 3).map((w, i) => {
              const native = /subagent/i.test(w)
              return (
                <div key={i} className="tw-break text-[10.5px] leading-relaxed text-foreground/80">
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
              <div className="text-[10.5px] text-muted-foreground">…还有 {warnings.length - 3} 条，见运行页</div>
            )}
          </div>
        )}
      </div>

      <div className="border-t border-border/60 px-3 py-1 text-[10px] text-muted-foreground">
        {pinned ? '已固定 · 点击空白处取消' : '单击节点固定详情 · 移开鼠标即隐藏'}
      </div>
    </div>
  )
}

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
    <div className={`min-w-0 ${className ?? ''}`} title={title}>
      <span className="text-muted-foreground">{label} </span>
      <span className={`break-all ${mono ? 'font-mono' : ''} text-foreground/85`}>{value}</span>
    </div>
  )
}
