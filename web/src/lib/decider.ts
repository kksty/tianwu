import type { DeciderStatus } from './types'

/** Chinese labels for the decider's live phase (internal/decider.Activity). */
export const PHASE_LABEL: Record<string, string> = {
  loading: '连线中',
  idle: '空闲',
  debounce: '去抖',
  board: '读事实意图图',
  model: '思考中',
  tool: '调用工具',
  apply: '执行动作',
  paused: '已暂停',
  blocked: '未就绪',
}

const ACTIVE_PHASES = new Set(['debounce', 'board', 'model', 'tool', 'apply'])

/** The phase to render, with the two states the server cannot express (the
 *  decider object is still loading / the operator paused it) resolved here. */
export function phaseOf(dec?: DeciderStatus): string {
  if (!dec) return 'loading'
  if (dec.paused) return 'paused'
  if (!dec.enabled) return 'blocked'
  return dec.activity?.phase || 'idle'
}

export function isPhaseActive(phase: string): boolean {
  return ACTIVE_PHASES.has(phase)
}

/**
 * A deliberately truncated progress bar: it approaches the end while the model
 * works but never claims the round is finished. A round that hangs should look
 * stuck, not complete.
 */
export function phaseProgress(seconds: number): number {
  if (seconds <= 0) return 0
  return Math.min(90, 100 * (1 - Math.exp(-seconds / 15)))
}

/** The one line under the phase: what the last/current round is doing. */
export function phaseDetail(dec?: DeciderStatus): string {
  if (!dec) return '等待控制面返回 board'
  const activity = dec.activity
  return (
    activity?.detail ||
    (dec.last_error ? `上一轮出错：${dec.last_error}` : '') ||
    (dec.enabled ? '等待下一次唤醒' : '指挥官未启用（检查模型配置或 --no-decider）')
  )
}
