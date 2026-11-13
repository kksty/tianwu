import type { Intent, Participant, Run } from './types.ts'

export interface RunGroup {
  key: string
  /** Newest first; grouping never drops older attempts. */
  runs: Run[]
}

export function runGroupKey(run: Run): string {
  // Support jobs can carry synthetic intent ids. Their purpose owns identity.
  if (run.purpose) return `purpose:${run.purpose}`
  if (run.intent_id) return `intent:${run.intent_id}`
  // Unattributed runs cannot be assumed to belong to the same executor.
  return `run:${run.id}`
}

export function groupRunHistory(runs: readonly Run[]): RunGroup[] {
  const groups = new Map<string, RunGroup>()
  const ordered = [...runs].sort((a, b) =>
    b.started_at.localeCompare(a.started_at) || b.id.localeCompare(a.id),
  )
  for (const run of ordered) {
    const key = runGroupKey(run)
    const group = groups.get(key)
    if (group) group.runs.push(run)
    else groups.set(key, { key, runs: [run] })
  }
  return [...groups.values()]
}

/** An explicit run pins history; choosing a group follows its latest run. */
export function selectRunHistory(groups: readonly RunGroup[], runID: string | null, groupKey: string | null) {
  const group = runID
    ? groups.find(g => g.runs.some(r => r.id === runID))
    : groupKey ? groups.find(g => g.key === groupKey) : groups[0]
  const run = runID ? group?.runs.find(r => r.id === runID) : group?.runs[0]
  return { group: group ?? null, run: run ?? null }
}

export type ExecutorKind = 'executor' | 'commander' | 'verifier' | 'report' | 'support'

/** The worker a run belongs to, phrased the way the operator asks about it:
 *  "指挥官发给哪个执行者". One logical participant per intent, reachable as
 *  pt_in_<intent id>, plus the harness session the newest attempt ran in. */
export interface RunExecutor {
  kind: ExecutorKind
  /** Primary name: the direction dispatched to, or the support role. */
  label: string
  /** Executor participant id (commander / pt_in_…); empty for support jobs. */
  participantID: string
  /** The newest attempt's harness session id, when recorded. */
  sessionID: string
  /** Secondary identity line, e.g. 「执行者 pt_in_a484fc1d · 会话 9da4e9bb76…」. */
  detail: string
}

/** Participants are the executors; index them by the intent they execute. */
export function participantsByIntent(people: readonly Participant[]): Map<string, Participant> {
  const byIntent = new Map<string, Participant>()
  for (const person of people) {
    if (person.intent_id) byIntent.set(person.intent_id, person)
  }
  return byIntent
}

function shortSession(id: string): string {
  return id.length > 10 ? `${id.slice(0, 10)}…` : id
}

export function runExecutor(
  run: Run,
  intent: Intent | undefined,
  byIntent: ReadonlyMap<string, Participant>,
): RunExecutor {
  const participant = run.intent_id ? byIntent.get(run.intent_id) : undefined
  const sessionID = run.session_id ?? ''
  const session = sessionID ? `会话 ${shortSession(sessionID)}` : ''
  switch (run.purpose) {
    case 'commander':
      return {
        kind: 'commander',
        label: '指挥官会话',
        participantID: 'commander',
        sessionID,
        detail: session || '未记录会话',
      }
    case 'verifier':
      return {
        kind: 'verifier',
        label: '复核运行',
        participantID: '',
        sessionID,
        detail: ['复核运行', session].filter(Boolean).join(' · '),
      }
    case 'report':
      return {
        kind: 'report',
        label: '报告运行',
        participantID: '',
        sessionID,
        detail: ['报告运行', session].filter(Boolean).join(' · '),
      }
    default:
      break
  }
  if (run.purpose) {
    return {
      kind: 'support',
      label: run.purpose,
      participantID: '',
      sessionID,
      detail: [run.purpose, session].filter(Boolean).join(' · '),
    }
  }
  const participantID = participant?.id ?? (run.intent_id ? `pt_${run.intent_id}` : '')
  const label = intent?.title || participant?.title || run.intent_id || '执行者'
  return {
    kind: 'executor',
    label,
    participantID,
    sessionID,
    detail: [participantID ? `执行者 ${participantID}` : '', session].filter(Boolean).join(' · '),
  }
}
