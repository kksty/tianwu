// Pure helpers for the run evidence view (Run.premise_claim_versions /
// context_fact_versions). They live here, not in the component, so the version
// semantics can be tested with node:test without a DOM or JSX pipeline.
import type { Fact, Run } from './types.ts'

export type EvidenceVersionKind = 'premise' | 'context'

export interface EvidenceVersionRow {
  id: string
  version: number
  kind: EvidenceVersionKind
}

export type VersionSetState = 'not_recorded' | 'empty' | 'rows'

/** undefined = the run predates version freezing; {} = frozen with no entries. */
export function versionSetState(versions?: Record<string, number>): VersionSetState {
  if (!versions) return 'not_recorded'
  return Object.keys(versions).length > 0 ? 'rows' : 'empty'
}

/** Stable display rows: fact id ascending, zero/absent versions kept honest. */
export function evidenceVersionRows(
  versions: Record<string, number> | undefined,
  kind: EvidenceVersionKind,
): EvidenceVersionRow[] {
  if (!versions) return []
  return Object.keys(versions)
    .sort((a, b) => (a < b ? -1 : a > b ? 1 : 0))
    .map((id) => ({ id, version: versions[id] ?? 0, kind }))
}

/** Premise rows freeze the assertion version; context rows a record revision. */
export function evidenceVersionLabel(kind: EvidenceVersionKind, version: number): string {
  if (!version || version <= 0) return '未记录'
  return kind === 'premise' ? `断言 v${version}` : `记录 r${version}`
}

/** The executor attempt for one direction, not a verifier/report support run. */
export function latestExecutorRun(runs: Run[], intentID: string): Run | undefined {
  let latest: Run | undefined
  for (const run of runs) {
    if (run.intent_id !== intentID || run.purpose) continue
    if (!latest || (run.started_at ?? '') > (latest.started_at ?? '')) latest = run
  }
  return latest
}

/** Resolve a fact id to its title; the id is the identity when it is not loaded. */
export function runEvidenceFactTitle(facts: Pick<Fact, 'id' | 'title'>[], id: string): string {
  return facts.find((fact) => fact.id === id)?.title ?? '事实不在当前列表'
}

/** What each frozen map answers, for the section tooltip. */
export function evidenceVersionHint(kind: EvidenceVersionKind): string {
  return kind === 'premise'
    ? '必要前提（from_fact_ids）的断言版本：结论依赖的就是这些版本'
    : '引用/固定（context_fact_ids、pinned_fact_ids）的账本记录版本：阅读指针，不是门槛'
}
