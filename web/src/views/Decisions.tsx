import { Badge, Callout, EmptyState, SkeletonList, StatusIndicator } from '@cyber/ui'
import { Page } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtInt, fmtTime } from '@/lib/format'
import { useLoad } from '@/lib/hooks'
import type { BoardResponse } from '@/lib/types'

export function Decisions({ board }: { board?: BoardResponse }) {
  const list = useLoad(() => api.decisions(100), [])
  const d = board?.decider

  const rows = list.data ?? []

  return (
    <Page
      title="决策"
      subtitle="局部任务自行推进；这里记录需要全局判断的原因、实际动作与用量。暂停或调用失败时，待处理事件会保留。"
    >
      {!d?.enabled && (
        <Callout tone="warning" className="mb-3">
          指挥官未启用：设置 CYBER_BASE_URL / CYBER_API_KEY / CYBER_MODEL 后重启，或加 --no-decider 明确只做事实意图图。
        </Callout>
      )}

      {list.loading && <SkeletonList count={5} />}
      {!list.loading && rows.length === 0 && (
        <EmptyState title="还没有决策记录" description="指挥官每轮写一行 decisions.jsonl；没配置模型时这里是空的。" />
      )}

      <div className="space-y-1.5">
        {rows.map((r, i) => (
          <div key={`${r.at}-${i}`} className="rounded-md border border-border bg-card px-3 py-2">
            <div className="flex flex-wrap items-center gap-2 text-[12px]">
              <span className="font-mono text-foreground">#{r.round}</span>
              <Badge size="sm" variant="secondary">
                {r.wake}
              </Badge>
              <span className="font-mono text-[11px] text-muted-foreground">{fmtTime(r.at)}</span>
              {r.session_rounds ? (
                <Badge size="sm" variant="muted">
                  会话 {r.session_rounds}
                </Badge>
              ) : null}
              <span className="ml-auto font-mono text-[11px] text-muted-foreground">
                {r.ms}ms · {fmtInt(r.tokens)} tok
              </span>
            </div>
            {(r.attention ?? []).length > 0 && (
              <div className="mt-2 rounded border border-border/60 bg-muted/20 px-2 py-1.5 text-[11px]" data-attention-reasons>
                <div className="mb-1 font-medium">本轮为何启动</div>
                <ul className="space-y-0.5">
                  {r.attention!.slice(0, 6).map((reason, j) => (
                    <li key={`${reason.kind}-${reason.id}-${j}`}>
                      {reason.detail}
                      {reason.intent_id && <span className="ml-1 text-muted-foreground">· {reason.intent_id}</span>}
                    </li>
                  ))}
                </ul>
                <div className="mt-1 text-muted-foreground">
                  {r.attention_delivered ? '本轮事件已交付；上下文确认与动作结果分别记录。' : '事件交付未完成，保留原记录等待恢复；不据此重放外部动作。'}
                </div>
              </div>
            )}
            <div className="mt-1 flex flex-wrap items-center gap-1.5">
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
            {r.error && <div className="tw-break mt-1 text-[12px] text-destructive">{r.error}</div>}
            {(r.tools ?? []).length > 0 && (
              <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
                <span>查询</span>
                {(r.tools ?? []).map((t, j) => (
                  <Badge key={j} size="sm" variant={t.error ? 'danger' : 'outline'}>
                    {t.tool}
                  </Badge>
                ))}
              </div>
            )}
            {(r.notes ?? []).length > 0 && (
              <ul className="mt-1 list-disc space-y-0.5 pl-4 text-[12px] text-muted-foreground">
                {r.notes!.map((n, j) => (
                  <li key={j}>{n}</li>
                ))}
              </ul>
            )}
          </div>
        ))}
      </div>

      {d?.enabled && (
        <div className="mt-3 flex flex-wrap items-center gap-3 text-[11px] text-muted-foreground">
          <StatusIndicator compact status="online" label={d.model ?? '模型'} />
          <span>{d.rounds} 轮</span>
          <span>{fmtInt(d.tokens_total)} tokens</span>
          <span>连续无新事实 {d.stalled}</span>
          {d.last_digest && <span className="font-mono">digest {d.last_digest.slice(0, 12)}</span>}
        </div>
      )}
    </Page>
  )
}
