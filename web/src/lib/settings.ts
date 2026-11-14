import type { ModelProfile, PremisePolicy, ProjectInfo } from './types.ts'

export function profileStatus(p: Pick<ModelProfile, 'runnable' | 'probed' | 'available' | 'probe_error' | 'paused' | 'probe_kind' | 'returned_model'>) {
  if (p.paused) return { label: '已暂停', tone: 'muted' as const, detail: '此配置不接受新运行，当前运行继续；默认调用自动选择其他可用模型，显式指定此配置需先恢复使用。' }
  if (!p.runnable) return { label: '配置不完整', tone: 'warning' as const, detail: '补齐端点、模型与凭据后才能执行。' }
  if (p.probe_kind === 'connectivity' && p.probed) {
    if (!p.available) return { label: '连接失败', tone: 'danger' as const, detail: p.probe_error || '未收到有效推理回复。' }
    return { label: '连接成功', tone: 'success' as const, detail: `已收到有效推理回复；响应模型：${p.returned_model || '未返回'}。工具调用未验证。` }
  }
  if (!p.probed) return { label: '待确认', tone: 'muted' as const, detail: p.probe_error || '尚未检测模型目录。' }
  if (!p.available) return { label: '检测失败', tone: 'danger' as const, detail: p.probe_error || '端点未返回这个模型。' }
  return { label: '目录已确认', tone: 'success' as const, detail: '模型存在于端点目录；不代表实际推理、工具调用已验证。' }
}

export function nonNegativeInteger(raw: string, label: string): number {
  const value = raw.trim()
  if (!value) return 0
  if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value))) throw new Error(`${label}必须是非负整数`)
  return Number(value)
}

export function durationLabel(seconds?: number): string {
  if (seconds == null) return '…'
  if (seconds <= 0) return '不限'
  if (seconds % 3600 === 0) return `${seconds / 3600}h`
  if (seconds % 60 === 0) return `${seconds / 60}m`
  return `${seconds}s`
}

export type ProjectSettingsDraft = {
  /** The one-paragraph project brief / authorization boundary. */
  scope: string
  /** The constraint document (built-in defaults, editable or cleared). */
  description: string
  worker_profile: string
  strong_profile: string
  max_runs: string
  round_timeout: string
  verify_auto: string
  /** Stored hard-premise admission mode; there is one value. */
  premise_policy: string
}

export function projectSettingsDraft(p: ProjectInfo): ProjectSettingsDraft {
  return {
    scope: p.scope ?? '',
    description: p.description ?? '',
    worker_profile: p.worker_profile ?? '', strong_profile: p.strong_profile ?? '',
    max_runs: p.max_runs == null ? '' : String(p.max_runs), round_timeout: p.round_timeout ?? '',
    verify_auto: p.verify_auto == null ? '' : p.verify_auto ? 'on' : 'off',
    premise_policy: 'verified_only',
  }
}

/** Refresh clean fields while preserving each field the operator has edited. */
export function mergeProjectSettings(previous: ProjectSettingsDraft, incoming: ProjectSettingsDraft, draft: ProjectSettingsDraft): ProjectSettingsDraft {
  const next = { ...draft }
  for (const key of Object.keys(incoming) as (keyof ProjectSettingsDraft)[]) {
    if (draft[key] === previous[key]) next[key] = incoming[key]
  }
  return next
}

/** Only changed fields are sent: saving concurrency must not overwrite routing.
 *  Clearing the constraint document is an explicit change ('' is sent, meaning
 *  the project carries no description), never an omitted field. */
export function projectSettingsPatch(previous: ProjectSettingsDraft, draft: ProjectSettingsDraft) {
  const patch: { scope?: string; description?: string; worker_profile?: string; strong_profile?: string; max_runs?: number | null; round_timeout?: string; verify_auto?: boolean | null; premise_policy?: PremisePolicy } = {}
  if (draft.scope !== previous.scope) patch.scope = draft.scope
  if (draft.description !== previous.description) patch.description = draft.description
  if (draft.worker_profile !== previous.worker_profile) patch.worker_profile = draft.worker_profile
  if (draft.strong_profile !== previous.strong_profile) patch.strong_profile = draft.strong_profile
  if (draft.max_runs !== previous.max_runs) patch.max_runs = draft.max_runs.trim() ? nonNegativeInteger(draft.max_runs, '项目并发') : null
  if (draft.round_timeout !== previous.round_timeout) patch.round_timeout = draft.round_timeout.trim()
  if (draft.verify_auto !== previous.verify_auto) patch.verify_auto = draft.verify_auto === '' ? null : draft.verify_auto === 'on'
  if (draft.premise_policy !== previous.premise_policy) patch.premise_policy = draft.premise_policy as PremisePolicy
  return patch
}
