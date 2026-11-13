import { Badge, Button, Callout, EmptyState, SkeletonList, StatusIndicator } from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtInt, fmtTime } from '@/lib/format'
import { useLoad } from '@/lib/hooks'
import type { BoardResponse } from '@/lib/types'

export function Decisions({ board }: { board?: BoardResponse }) {
  const list = useLoad(() => api.decisions(100), [])
  const d = board?.decider

  const rows = list.data ?? []
  const firstLoad = list.loading && !list.data

  return (
    <Page
      title="决策"
      subtitle="局部任务自行推进；这里记录需要全局判断的原因、实际动作与用量。暂停或调用失败时，待处理事件会保留。"
      meta={
        d?.enabled ? (
          <>
            <span className="tw-nums font-mono text-[11px] text-ink-3">{fmtInt(d.rounds)} 轮</span>
            <span className="tw-nums font-mono text-[11px] text-ink-3">{fmtInt(d.tokens_total)} tokens</span>
          </>
        ) : undefined
      }
      actions={
        <Button size="sm" variant="outline" disabled={list.loading} onClick={() => list.reload()}>
          刷新
        </Button>
      }
    >
      {!d?.enabled && (
        <Callout tone="warning" className="mb-4">
          指挥官未启用：设置 CYBER_BASE_URL / CYBER_API_KEY / CYBER_MODEL 后重启，或加 --no-decider 明确只做事实意图图。
        </Callout>
      )}

      {list.error && (
        <div className="mb-4">
          <Callout tone="destructive">
            <div className="flex flex-wrap items-center gap-2">
              <span>读决策记录失败：{list.error}</span>
              <Button size="xs" variant="outline" onClick={() => list.reload()}>重试</Button>
            </div>
          </Callout>
        </div>
      )}

      {firstLoad && <SkeletonList count={5} />}

      {!firstLoad && !list.error && rows.length === 0 && (
        <EmptyState
          title="还没有决策记录"
          description="指挥官每轮写一行 decisions.jsonl；没配置模型时这里是空的。配置模型并开始一轮后，这里会出现启动原因、动作与用量。"
          action={<Button size="sm" variant="outline" onClick={() => list.reload()}>刷新</Button>}
        />
      )}

      {rows.length > 0 && (
        <Panel
          title="决策记录"
          extra={<span className="tw-nums font-mono text-[11px] text-ink-3">{fmtInt(rows.length)} 条 · 最近在前</span>}
          bodyClassName="p-0"
        >
          <div className="divide-y divide-hairline">
            {rows.map((r, i) => (
              <article key={`${r.at}-${i}`} className="px-4 py-3 transition-colors hover:bg-surface-2/40">
                {/* Row head: round → wake → time … cost. Numbers are mono. */}
                <div className="flex flex-wrap items-center gap-2">
                  <span className="tw-nums font-mono text-[12px] font-medium text-ink">#{r.round}</span>
                  <Badge size="sm" variant="secondary">
                    {r.wake}
                  </Badge>
                  <span className="tw-nums font-mono text-[11px] text-ink-3">{fmtTime(r.at)}</span>
                  {r.session_rounds ? (
                    <span className="tw-nums font-mono text-[11px] text-ink-3">会话 {r.session_rounds}</span>
                  ) : null}
                  <span className="tw-nums ml-auto shrink-0 font-mono text-[11px] text-ink-3">
                    {r.ms}ms · {fmtInt(r.tokens)} tok
                  </span>
                </div>

                {/* Why this round woke: the reasoning record, recessed. */}
                {(r.attention ?? []).length > 0 && (
                  <div className="tw-inset mt-2 rounded-md px-2.5 py-2" data-attention-reasons>
                    <div className="mb-1 text-[11px] font-medium leading-4 text-ink-3">本轮为何启动</div>
                    <ul className="space-y-1 text-[12px] leading-relaxed text-ink-2">
                      {r.attention!.slice(0, 6).map((reason, j) => (
                        <li key={`${reason.kind}-${reason.id}-${j}`} className="tw-break">
                          {reason.detail}
                          {reason.intent_id && <span className="ml-1 font-mono text-[11px] text-ink-3">· {reason.intent_id}</span>}
                        </li>
                      ))}
                    </ul>
                    <div className="mt-1.5 border-t border-hairline pt-1.5 text-[11px] leading-4 text-ink-3">
                      {r.attention_delivered ? '本轮事件已交付；上下文确认与动作结果分别记录。' : '事件交付未完成，保留原记录等待恢复；不据此重放外部动作。'}
                    </div>
                  </div>
                )}

                {/* Actions taken this round. */}
                {(r.actions ?? []).length > 0 && (
                  <div className="mt-2 flex flex-wrap items-center gap-1.5">
                    {(r.actions ?? []).map((a, j) => (
                      <Badge key={j} size="sm" variant={a.kind === 'error' ? 'danger' : a.kind === 'intent' ? 'primary' : 'muted'}>
                        {a.kind}
                        {a.text ? `: ${a.text}` : a.id ? ` ${a.id.slice(0, 8)}` : ''}
                      </Badge>
                    ))}
                    {r.fallback && (
                      <Badge size="sm" variant="warning">
                        降级 {r.fallback}
                      </Badge>
                    )}
                  </div>
                )}

                {r.error && (
                  <div role="alert" className="tw-break mt-2 rounded-md border border-destructive/25 bg-destructive/[0.07] px-2.5 py-1.5 text-[12px] leading-relaxed text-destructive">
                    {r.error}
                  </div>
                )}

                {/* Tools queried, then free-form notes: quieter than actions. */}
                {(r.tools ?? []).length > 0 && (
                  <div className="mt-2 flex flex-wrap items-center gap-1.5 border-t border-hairline pt-2 text-[11px] text-ink-3">
                    <span>查询</span>
                    {(r.tools ?? []).map((t, j) => (
                      <Badge key={j} size="sm" variant={t.error ? 'danger' : 'outline'} className="font-mono">
                        {t.tool}
                      </Badge>
                    ))}
                  </div>
                )}
                {(r.notes ?? []).length > 0 && (
                  <ul className="mt-2 list-disc space-y-0.5 border-t border-hairline pl-4 pt-2 text-[12px] leading-relaxed text-ink-2">
                    {r.notes!.map((n, j) => (
                      <li key={j} className="tw-break">{n}</li>
                    ))}
                  </ul>
                )}
              </article>
            ))}
          </div>

          {/* Footer: the decider's own counters, one quiet 11px line. */}
          {d?.enabled && <DeciderStatusLine d={d} />}
        </Panel>
      )}

      {/* A fresh project has an enabled decider but no decisions yet: keep its
          live counters visible instead of hiding them with the empty list. */}
      {!firstLoad && !list.error && rows.length === 0 && d && d.enabled && (
        <Panel title="指挥官状态" bodyClassName="p-0">
          <DeciderStatusLine d={d} />
        </Panel>
      )}
    </Page>
  )
}

/** Decider counters: model, rounds, tokens, stall run and the last digest. */
function DeciderStatusLine({ d }: { d: NonNullable<BoardResponse['decider']> }) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-hairline px-4 py-2.5 text-[11px] text-ink-3">
      <StatusIndicator compact status="online" label={d.model ?? '模型'} />
      <span className="tw-nums font-mono">{fmtInt(d.rounds)} 轮</span>
      <span className="tw-nums font-mono">{fmtInt(d.tokens_total)} tokens</span>
      <span className="tw-nums">连续无新事实 {d.stalled}</span>
      {d.last_digest && <span className="truncate font-mono">digest {d.last_digest.slice(0, 12)}</span>}
    </div>
  )
}
