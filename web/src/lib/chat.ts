import { statusLabel } from './format.ts'
import type { ChatResult, ChatTarget } from './types'

/** "all" is the commander plus executors; "commander" is the commander alone. */
export function chatIncludesExecutors(target: ChatTarget): boolean {
  return target === 'all'
}

export interface ChatSummary {
  /** Commander receipt (one ledger row on the real inbox path). */
  commander: string
  /** Executor half: live vs queued counts, or why nobody could receive. */
  executors: string
  /** Finished/parked directions that intentionally did not receive the turn. */
  skipped: string
}

/**
 * Operator-facing receipt lines for one chat turn. Pure so the 通信 page and its
 * tests agree on delivery words: "queued" means the recipient's next run reads
 * it, "skipped" means the direction is settled and chat does not revive it.
 */
export function chatSummary(result: ChatResult): ChatSummary {
  const commanderStatus =
    result.commander.status === 'queued'
      ? '排队，指挥官下一轮可见'
      : result.commander.status === 'accepted'
        ? '已送入指挥官现场'
        : result.commander.status
  const commander = `指挥官：${commanderStatus}（${result.commander.message_id}）`
  const live = result.delivered.filter((d) => d.status === 'accepted' || d.status === 'confirmed').length
  const queued = result.delivered.length - live
  let executors = ''
  if (chatIncludesExecutors(result.to)) {
    executors = result.delivered.length === 0
      ? '执行者：当前没有可接收的执行者'
      : `执行者：${live} 个已送达${queued > 0 ? `，${queued} 个排队（下一轮可见）` : ''}`
  }
  const names = result.skipped
    .slice(0, 3)
    .map((s) => `${s.title || s.intent_id}（${statusLabel(s.status)}）`)
  const skipped = result.skipped.length > 0
    ? `跳过 ${result.skipped.length} 个已结束/停放方向：${names.join('、')}${result.skipped.length > 3 ? '…' : ''}`
    : ''
  return { commander, executors, skipped }
}
