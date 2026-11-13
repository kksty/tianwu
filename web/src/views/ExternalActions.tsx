import { useState } from 'react'
import { ArrowUpRight, Loader2, RefreshCw, Search } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  EmptyState,
  Field,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SkeletonList,
  StatusIndicator,
  Textarea,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { usePoll } from '@/lib/hooks'
import { fmtTime } from '@/lib/format'
import type { ExternalAction } from '@/lib/types'

const labels = {started:'已登记 · 等待结果',completed:'已记录结果',unknown:'结果未知 · 需核对'} as const

/**
 * External actions are records, not controls: the card states what was done
 * outside the console and what came back. Each entry is one Panel (header =
 * name + state + version, body = ids / result / record form), so the reading
 * order is "what, then evidence, then act".
 */
export function ExternalActions() {
  const list = usePoll(api.externalActions, 4000)
  const [query,setQuery] = useState('')
  const [editing,setEditing] = useState<ExternalAction>()
  const [result,setResult] = useState('')
  const [reference,setReference] = useState('')
  const [state,setState] = useState<'completed'|'unknown'>('completed')
  const [busy,setBusy] = useState(false)
  const [error,setError] = useState('')
  const rows = [...(list.data?.actions ?? [])].reverse().filter(a => `${a.id} ${a.name} ${a.intent_id} ${a.result ?? ''}`.toLowerCase().includes(query.trim().toLowerCase()))
  const refreshing = list.loading || list.refreshing
  async function save() {
    if (!editing || !result.trim()) return
    setBusy(true);setError('')
    try {
      await api.resolveAction(editing.id,{version:editing.version,state,result:result.trim(),result_ref:reference.trim() || undefined})
      setEditing(undefined);list.reload()
    } catch(e) {setError(e instanceof Error ? e.message : String(e));list.reload()}
    finally {setBusy(false)}
  }
  return (
    <Page
      title="外部动作"
      subtitle="保存动作状态与外部结果；明确等待这些动作的方向才会恢复，暂停期间结果保留排队。"
      actions={
        <Button size="sm" variant="outline" onClick={list.reload} disabled={refreshing}>
          <RefreshCw className={refreshing ? 'h-3.5 w-3.5 animate-spin' : 'h-3.5 w-3.5'} /> 刷新
        </Button>
      }
    >
      <div className="mx-auto max-w-5xl space-y-4">
        <Callout tone="info">
          这里记录执行状态，不判断渗透策略或漏洞结论。“已记录结果”也可能是失败结果；“结果未知”需核对外部状态后再决定下一步。提交结果不会重新执行动作。
        </Callout>

        <div className="flex flex-wrap items-center gap-2">
          <div className="relative w-full min-w-[200px] sm:w-[320px]">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" />
            <Input
              aria-label="搜索外部动作"
              placeholder="搜索动作、方向或结果…"
              value={query}
              onChange={e=>setQuery(e.target.value)}
              className="h-8 w-full pl-7 text-[12px]"
            />
          </div>
          <span className="tw-nums ml-auto font-mono text-[11px] text-ink-3">
            显示 {rows.length} / {list.data?.actions?.length ?? 0}
          </span>
        </div>

        {(list.error || error) && (
          <Callout tone="destructive">
            {error || list.error}
            <Button size="xs" variant="outline" className="ml-2" onClick={list.reload}>
              重试
            </Button>
          </Callout>
        )}
        {list.loading && <SkeletonList count={3} />}
        {!list.loading && list.data && rows.length===0 && (
          <Panel bodyClassName="p-0">
            <EmptyState
              title="没有外部动作"
              description={query ? '没有匹配的记录。' : '执行者显式登记外部动作后，会在这里显示状态和结果。'}
              action={query ? <Button size="sm" variant="outline" onClick={() => setQuery('')}>清除搜索</Button> : undefined}
            />
          </Panel>
        )}
        {rows.map(a=>(
          <article key={a.id} className="tw-panel overflow-hidden rounded-xl">
            <header className="flex flex-wrap items-center gap-2 border-b border-hairline px-4 py-3">
              <h2 className="tw-break tw-section-title min-w-0 flex-1">{a.name}</h2>
              <StatusIndicator compact status={a.state==='completed'?'idle':a.state==='unknown'?'warning':'running'} label={labels[a.state]} />
              <Badge variant="outline" size="sm">版本 <span className="tw-nums font-mono">{a.version}</span></Badge>
            </header>
            <div className="space-y-2.5 p-4">
              <div className="flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-[11px] leading-4 text-ink-3">
                <span className="tw-break">{a.id}</span>
                <a href={`?view=state&intent=${encodeURIComponent(a.intent_id)}`} className="inline-flex min-w-0 items-center gap-1 text-ink-2 underline-offset-2 hover:text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
                  <span className="truncate">{a.intent_id}</span><ArrowUpRight className="h-3 w-3 shrink-0" />
                </a>
                <a href={`?view=runs&run=${encodeURIComponent(a.run_id)}`} className="truncate text-ink-2 underline-offset-2 hover:text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">{a.run_id}</a>
                <span className="tw-nums ml-auto">{fmtTime(a.updated_at)}</span>
              </div>
              {a.reference && (
                <p className="tw-break text-[12px] leading-relaxed text-ink-2">外部引用：{a.reference}</p>
              )}
              {a.result && (
                <pre className="tw-inset tw-break max-h-64 overflow-auto whitespace-pre-wrap rounded-lg p-3 font-mono text-[11px] leading-relaxed text-ink-2">{a.result}</pre>
              )}
              {a.result_ref && (
                <p className="tw-break text-[11px] leading-4 text-ink-3">结果引用：{a.result_ref}</p>
              )}
              {editing?.id===a.id ? (
                <div className="space-y-2.5 rounded-lg border border-hairline bg-surface-2/40 p-3">
                  <Field label="记录状态">
                    <Select value={state} onValueChange={v=>setState(v as 'completed'|'unknown')} disabled={busy}>
                      <SelectTrigger className="h-8 text-[12px]" aria-label="动作结果状态">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="completed">已取得结果</SelectItem>
                        <SelectItem value="unknown">结果尚不确定</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                  <Textarea aria-label="动作结果或未知原因" placeholder="填写已核对的结果，或说明为何尚不确定" value={result} onChange={e=>setResult(e.target.value)} disabled={busy} className="text-[12px]" />
                  <Input aria-label="动作结果引用" placeholder="结果引用（可选）" value={reference} onChange={e=>setReference(e.target.value)} disabled={busy} className="h-8 text-[12px]" />
                  <div className="flex flex-wrap gap-2">
                    <Button size="sm" disabled={busy || !result.trim()} onClick={()=>void save()}>
                      {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}
                      {busy?'提交中…':'记录结果'}
                    </Button>
                    <Button size="sm" variant="ghost" disabled={busy} onClick={()=>setEditing(undefined)}>取消</Button>
                  </div>
                  <p className="text-[11px] leading-4 text-ink-3">
                    提交版本 <span className="font-mono">{editing.version}</span>。若版本已变化，请取消后重新核对最新记录。
                  </p>
                </div>
              ) : a.state!=='completed' ? (
                <Button size="sm" variant="outline" onClick={()=>{setEditing(a);setResult('');setReference('');setState('completed');setError('')}}>核对并记录结果</Button>
              ) : null}
            </div>
          </article>
        ))}
      </div>
    </Page>
  )
}
