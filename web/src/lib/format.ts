import type { BadgeProps } from '@cyber/ui'
import type { StatusKind } from '@cyber/ui'
import type { FrontierItem, Intent, Run } from './types'

type BadgeVariant = NonNullable<BadgeProps['variant']>

/** Intent / run status → the status chip's visual language. */
export function statusKind(status?: string): StatusKind {
  switch (status) {
    case 'running':
    case 'claimed':
      return 'running'
    case 'pending':
    case 'waiting':
      return 'pending'
    case 'done':
    case 'ready':
    case 'pass':
    case 'verified':
      return 'success'
    case 'failed':
    case 'error':
    case 'reject':
    case 'rejected':
      return 'error'
    case 'canceled':
    case 'stale':
    case 'needs_evidence':
      return 'warning'
    case 'parked':
    case 'unverified':
      return 'idle'
    default:
      return 'info'
  }
}

const statusLabels: Record<string, string> = {
  pending: '待办',
  claimed: '认领',
  running: '运行中',
  waiting: '等待子任务',
  done: '完成',
  failed: '失败',
  parked: '停放',
  canceled: '已取消',
  ready: '可用',
  stale: '已过期',
  unverified: '未复核',
  verified: '已复核',
  rejected: '已否决',
  pass: '通过',
  needs_evidence: '缺证据',
  reject: '否决',
}

export function statusLabel(status?: string): string {
  if (!status) return '—'
  return statusLabels[status] ?? status
}

/**
 * What a run is for. An empty purpose is an executor attempt (it must have an
 * intent); commander/verifier/report are support runs that belong to no
 * direction, so the run page has to say which is which in words, not raw enum.
 */
export function purposeLabel(purpose?: string): string {
  switch (purpose) {
    case 'commander':
      return '指挥官'
    case 'verifier':
      return '复核'
    case 'report':
      return '报告'
    case 'embedding':
      return '检索向量化'
    case '':
    case undefined:
      return ''
    default:
      return purpose
  }
}

/** Run statuses need one more distinction: a steer is a continuation, not a stop. */
export function runStatusLabel(run?: { status?: string; cancel_cause?: string }): string {
  if (run?.status === 'canceled') {
    switch (run.cancel_cause) {
      case 'steer':
        return '转续话'
      case 'stop_loss':
        return '止损取消'
      case 'handoff':
        return '交接取消'
      case 'restart':
        return '重启取消'
      case 'watchdog':
        return '守卫取消'
      case 'operator':
        return '人工取消'
      case 'retry':
        return '重派取消'
      case 'worker_kill':
        return '被终止'
      case 'operator_wait':
        return '等待人工'
      case 'action_wait':
        return '等待外部结果'
      case 'child_wait':
        return '等待子任务'
      case 'pause':
        return '项目暂停'
      case 'model_stall':
        return '模型停滞'
      case 'round_timeout':
        return '单轮超时'
    }
  }
  return statusLabel(run?.status)
}

/**
 * One sentence for *why* a canceled run was stopped, for the tooltip behind the
 * short label. The label is the same table runStatusLabel uses; this adds the
 * mechanism (who decides, what happens next), not a second vocabulary.
 */
export function cancelCauseText(cause?: string): string {
  switch (cause) {
    case 'steer':
      return '指挥官取消当前 turn、带轨迹续一轮：会话与已写证据保留'
    case 'retry':
      return '重派：本次 run 让位给同方向的新一次执行'
    case 'stop_loss':
      return '止损：连续多轮没有新事实或派发，控制面主动停止'
    case 'handoff':
      return '换模型接班：旧 run 先停，新模型从同一方向继续'
    case 'restart':
      return '控制面重启时仍未结束，进程重启前统一取消'
    case 'watchdog':
      return '运行守卫超时：超过时限没有事件，防止空转'
    case 'operator':
      return '操作员在控制台人工取消（或重派前取消旧 run）'
    case 'worker_kill':
      return 'worker 被 kill：执行者/操作员主动终止，方向不再自动重派'
    case 'operator_wait':
      return '人工交接：保留现场并释放执行槽，操作员完成后继续'
    case 'action_wait':
      return '等待外部动作：保留会话并释放执行槽，只在匹配结果或超时后继续'
    case 'child_wait':
      return '等待子任务：保留会话并释放执行槽，条件满足后携带结果继续'
    case 'pause':
      return '项目暂停：停止指挥官与所有执行者'
    case 'review_pause':
      return '复核暂停：取消正在跑的复核，不动指挥官与执行者（项目与复核是两个开关）'
    case 'model_stall':
      return '模型停滞：连续无有效输出，存活检查停止该 run'
    case 'round_timeout':
      return '指挥官单轮超过墙钟上限，控制面取消该轮'
    case '':
    case undefined:
      return ''
    default:
      return `取消原因：${cause}`
  }
}

/**
 * The visible "取消原因" line for a canceled run. exit_reason is what the
 * canceller actually recorded (e.g. "worker kill：operator", "项目停止：复核被取消");
 * prefer it verbatim, and say so plainly when older rows have none.
 */
export function runCancelReason(run?: {
  status?: string
  cancel_cause?: string
  exit_reason?: string
}): string {
  if (run?.status !== 'canceled') return ''
  const label = runStatusLabel(run)
  const raw = (run.exit_reason ?? '').trim()
  return raw && raw !== label ? `${label} · ${raw}` : `${label}（原因未记录）`
}

const kindVariants: Record<string, BadgeVariant> = {
  vuln: 'danger',
  cred: 'caution',
  host: 'info',
  service: 'info',
  loot: 'success',
  artifact: 'primary',
  note: 'muted',
}

export function kindVariant(kind?: string): BadgeVariant {
  return kindVariants[kind ?? ''] ?? 'muted'
}

const originVariants: Record<string, BadgeVariant> = {
  declared: 'success',
  derived: 'info',
  heuristic: 'warning',
}

export function originVariant(origin?: string): BadgeVariant {
  return originVariants[origin ?? ''] ?? 'secondary'
}

/** 2026-11-01T02:03:04+08:00 → 11-01 02:03:04 (local, no timezone noise). */
export function fmtTime(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function fmtDuration(from?: string, to?: string): string {
  if (!from) return '—'
  const start = new Date(from).getTime()
  const end = to ? new Date(to).getTime() : Date.now()
  if (Number.isNaN(start) || Number.isNaN(end)) return '—'
  const s = Math.max(0, Math.round((end - start) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`
}

export function fmtBytes(n?: number): string {
  if (!n || n < 0) return '—'
  const units = ['B', 'K', 'M', 'G', 'T']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)}${units[i]}`
}

export function fmtInt(n?: number): string {
  if (n === undefined || n === null) return '0'
  return n.toLocaleString('en-US')
}

/** Seconds → "3h12m" / "42m"; the unattended uptime chip. */
export function fmtUptime(seconds?: number): string {
  if (!seconds || seconds <= 0) return '—'
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (h > 0) return `${h}h${String(m).padStart(2, '0')}m`
  return `${Math.max(1, m)}m`
}

/** First line, clipped — for table cells that must stay one row. */
export function oneLine(s?: string, max = 120): string {
  if (!s) return ''
  const line = s.split(/\r?\n/).find((l) => l.trim()) ?? ''
  return line.length > max ? `${line.slice(0, max)}…` : line
}

export function shortID(id?: string): string {
  if (!id) return '—'
  return id.length > 12 ? id.slice(0, 12) : id
}

/**
 * True only when the direction cites premise facts that are gone or rejected.
 * `frontier.ready` is also false for failed/held directions (they cannot be
 * dispatched either), so readiness alone is not a premise signal — the blocked
 * list is. The badge must not call every non-dispatchable direction a broken
 * premise.
 */
export function hasBrokenPremise(frontier?: FrontierItem): boolean {
  return (frontier?.blocked_on?.length ?? 0) > 0
}

/** One badge-sized marker on a frontier row; `detail` is the full tooltip. */
export interface FrontierMarker {
  key: 'premise_changed' | 'no_premise' | 'descendants' | 'prerequisites'
  label: string
  detail: string
  tone: 'warning' | 'info' | 'muted' | 'secondary'
}

/** Compact fact-id list for a tooltip/one-line hint ("f_a、f_b 等 4 条"). */
export function frontierFactList(ids?: string[], cap = 3): string {
  const list = (ids ?? []).filter(Boolean)
  if (list.length === 0) return ''
  const shown = list.slice(0, cap)
  const hidden = list.length - shown.length
  return hidden > 0 ? `${shown.join('、')} 等 ${list.length} 条` : shown.join('、')
}

/**
 * Badges for one frontier row. Only fields the server actually reported are
 * emitted: an old board that predates the new fields yields no marker rather
 * than a guessed "无前提" or "0 前提". `no_premise` and `prerequisites` are read
 * independently so a malformed row is shown as reported instead of corrected.
 */
export function frontierMarkers(frontier?: FrontierItem): FrontierMarker[] {
  if (!frontier) return []
  const out: FrontierMarker[] = []
  const changed = frontier.premise_changed ?? []
  if (changed.length > 0) {
    out.push({
      key: 'premise_changed',
      label: `前提已变 ${changed.length}`,
      detail: `方向创建后，这些前提事实的断言被修改过：${frontierFactList(changed, 4)}。旧断言上的复核不能证明新内容；派发前请核对前提或重做复核。`,
      tone: 'warning',
    })
  }
  if (typeof frontier.prerequisites === 'number' && frontier.prerequisites > 0) {
    out.push({
      key: 'prerequisites',
      label: `前提 ${frontier.prerequisites}`,
      detail: `该方向依赖 ${frontier.prerequisites} 条硬前提（from_fact_ids）；缺一条或前提被否决都不能派发。`,
      tone: 'muted',
    })
  }
  if (frontier.no_premise) {
    out.push({
      key: 'no_premise',
      label: '无前提',
      detail:
        '该方向没有硬前提（from_fact_ids 为空），可以直接派发；引用/固定只是阅读上下文，不是前提。',
      tone: 'info',
    })
  }
  if (typeof frontier.descendants === 'number' && frontier.descendants > 0) {
    out.push({
      key: 'descendants',
      label: `后代 ${frontier.descendants}`,
      detail: `该方向下还有 ${frontier.descendants} 个未结束的后代方向；父方向可能在等待它们，先看子方向再决定是否重派。`,
      tone: 'secondary',
    })
  }
  return out
}

/** One-line premise-changed warning for the row body; '' when nothing changed. */
export function frontierPremiseChangedText(frontier?: FrontierItem): string {
  const changed = frontier?.premise_changed ?? []
  return changed.length > 0 ? `前提已变：${frontierFactList(changed)}` : ''
}

/** Human label for the project premise policy (standard/verified_only). */
export function premisePolicyLabel(policy?: string): string {
  switch (policy) {
    case 'verified_only':
      return '证明优先'
    case 'standard':
      return '标准'
    default:
      return policy || '—'
  }
}

/** What a policy actually enforces, in the operator's words. */
export function premisePolicyHint(policy?: string): string {
  switch (policy) {
    case 'verified_only':
      return '全部必要前提必须是可复核类型，且当前断言已独立核验、核验收据绑定当前断言、原件完整；待核验依赖可先登记，核验前不能派发。必要前提会自动排队核验。'
    case 'standard':
      return '前提事实存在且未被否决即可创建/派发（历史行为）；未复核的 vuln/cred 仍可当硬前提。'
    default:
      return ''
  }
}

/**
 * The run page's filter. One box has to answer both kinds of question an
 * operator arrives with: "what is r_abc?" (run id) and "where did in_abc go?"
 * (intent id/title), plus the fields people remember (purpose, model, node).
 */
export function runMatchesFilter(run: Run, intent: Intent | undefined, needle: string): boolean {
  const q = needle.trim().toLowerCase()
  if (!q) return true
  return [
    run.id,
    run.intent_id,
    intent?.title,
    run.purpose,
    purposeLabel(run.purpose),
    run.model,
    run.resolved_profile,
    run.node,
    run.status,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(q)
}
