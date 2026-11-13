// What one run actually read, frozen at dispatch.
//
// A run carries two version maps from PrepareRunInput (internal/store/input.go):
//   - premise_claim_versions: for the hard premises (from_fact_ids), the
//     assertion version a reviewer would need to reproduce the conclusion;
//   - context_fact_versions: for explicit references/pins, the ledger record
//     revision the dispatch snapshot held — a reading pointer, not a premise.
// The maps are frozen when the run is dispatched, so later edits never rewrite
// them. The view is read-only: editing context is a separate explicit act.
//
// An absent map (old run) is not the same as an empty map (run recorded that it
// had nothing): versionSetState keeps them distinguishable instead of printing
// "0 versions" for both. The pure helpers live in lib/run-evidence.ts.
import { Badge } from '@cyber/ui'
import {
  evidenceVersionHint,
  evidenceVersionLabel,
  evidenceVersionRows,
  runEvidenceFactTitle,
  versionSetState,
  type EvidenceVersionKind,
} from '@/lib/run-evidence'
import type { Fact, Run } from '@/lib/types'

export function RunEvidenceView({
  run,
  facts,
  onSelectFact,
}: {
  run: Run
  facts: Pick<Fact, 'id' | 'title'>[]
  onSelectFact?: (factID: string) => void
}) {
  return (
    <div className="tw-inset space-y-2 rounded-md px-2.5 py-2" data-run-evidence>
      <div className="flex items-center gap-2">
        <span className="tw-eyebrow normal-case tracking-normal">本次运行读到的版本</span>
        <span className="ml-auto min-w-0 truncate font-mono text-[11px] text-ink-3" title={run.id}>
          {run.id}
        </span>
      </div>

      <div className="space-y-2 divide-y divide-hairline [&>div+div]:pt-2">
        <VersionBlock
          title="必要前提断言版本"
          hint="冻结于派发；前提是新结论依赖的硬门槛"
          kind="premise"
          versions={run.premise_claim_versions}
          facts={facts}
          onSelectFact={onSelectFact}
          emptyNote="这次运行没有硬前提（from_fact_ids 为空）"
          absentNote="旧运行没有记录前提断言版本"
        />
        <VersionBlock
          title="上下文记录版本"
          hint="冻结于派发；引用/固定只影响阅读，不是派发门槛"
          kind="context"
          versions={run.context_fact_versions}
          facts={facts}
          onSelectFact={onSelectFact}
          emptyNote="这次运行没有显式引用或固定事实"
          absentNote="旧运行没有记录上下文版本"
        />
      </div>

      <div className="border-t border-hairline pt-1.5 text-[11px] leading-relaxed text-ink-3">
        版本与事实内容在派发时冻结；之后修改事实不会改写这里。断言版本看断言是否还是同一句，记录版本看账本行是否被改过。
      </div>
    </div>
  )
}

function VersionBlock({
  title,
  hint,
  kind,
  versions,
  facts,
  onSelectFact,
  emptyNote,
  absentNote,
}: {
  title: string
  hint: string
  kind: EvidenceVersionKind
  versions: Record<string, number> | undefined
  facts: Pick<Fact, 'id' | 'title'>[]
  onSelectFact?: (factID: string) => void
  emptyNote: string
  absentNote: string
}) {
  const rows = evidenceVersionRows(versions, kind)
  const state = versionSetState(versions)
  return (
    <div>
      <div className="flex items-center gap-2" title={evidenceVersionHint(kind)}>
        <span className="text-[12px] font-medium text-ink-2">{title}</span>
        <Badge size="sm" variant="muted" className="tw-nums font-mono">{rows.length}</Badge>
        <span className="truncate text-[11px] text-ink-3">{hint}</span>
      </div>
      {state === 'not_recorded' && (
        <div className="mt-1 rounded border border-dashed border-hairline px-2 py-1 text-[11px] text-ink-3">
          {absentNote}
        </div>
      )}
      {state === 'empty' && <div className="mt-1 text-[11px] text-ink-3">{emptyNote}</div>}
      {state === 'rows' && (
        <div className="mt-1 space-y-0.5">
          {rows.map((row) => (
            <div key={`${row.kind}:${row.id}`} className="flex items-center gap-2">
              {onSelectFact ? (
                <button
                  type="button"
                  onClick={() => onSelectFact(row.id)}
                  className="min-w-0 flex-1 truncate rounded text-left text-[12px] text-info transition-colors hover:bg-surface-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                  title={`查看事实 ${row.id}`}
                >
                  {runEvidenceFactTitle(facts, row.id)}
                </button>
              ) : (
                <span className="min-w-0 flex-1 truncate text-[12px] text-ink-2">
                  {runEvidenceFactTitle(facts, row.id)}
                </span>
              )}
              <Badge
                size="sm"
                variant={row.version > 0 ? 'primary' : 'muted'}
                className="tw-nums shrink-0 font-mono"
                title={row.version > 0 ? `${row.id} 的冻结版本` : `${row.id} 没有记录版本`}
              >
                {evidenceVersionLabel(row.kind, row.version)}
              </Badge>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
