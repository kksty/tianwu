import { useState } from 'react'
import { ArrowUpRight, RefreshCw } from 'lucide-react'
import { Badge, Button, Callout, EmptyState, Input, SkeletonList, StatusIndicator, Textarea } from '@cyber/ui'
import { Page } from '@/components/Page'
import { api } from '@/lib/api'
import { usePoll } from '@/lib/hooks'
import { fmtTime } from '@/lib/format'
import type { ExternalAction } from '@/lib/types'

const labels = {started:'已登记 · 等待结果',completed:'已记录结果',unknown:'结果未知 · 需核对'} as const

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
  async function save() {
    if (!editing || !result.trim()) return
    setBusy(true);setError('')
    try {
      await api.resolveAction(editing.id,{version:editing.version,state,result:result.trim(),result_ref:reference.trim() || undefined})
      setEditing(undefined);list.reload()
    } catch(e) {setError(e instanceof Error ? e.message : String(e));list.reload()}
    finally {setBusy(false)}
  }
  return <Page title="外部动作" subtitle="保存动作状态与外部结果；明确等待这些动作的方向才会恢复，暂停期间结果保留排队。" actions={<Button size="sm" variant="outline" onClick={list.reload} disabled={list.loading || list.refreshing}><RefreshCw className="mr-1 h-3 w-3" />刷新</Button>}>
    <div className="mx-auto max-w-5xl space-y-4">
      <Callout tone="info">这里记录执行状态，不判断渗透策略或漏洞结论。“已记录结果”也可能是失败结果；“结果未知”需核对外部状态后再决定下一步。提交结果不会重新执行动作。</Callout>
      <Input aria-label="搜索外部动作" placeholder="搜索动作、方向或结果…" value={query} onChange={e=>setQuery(e.target.value)} />
      {(list.error || error) && <Callout tone="destructive">{error || list.error}</Callout>}
      {list.loading && <SkeletonList count={3} />}
      {!list.loading && rows.length===0 && <EmptyState title="没有外部动作" description={query ? '没有匹配的记录。' : '执行者显式登记外部动作后，会在这里显示状态和结果。'} />}
      {rows.map(a=><article key={a.id} className="rounded-lg border border-border bg-card p-4">
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="min-w-0 flex-1 break-words text-sm font-medium">{a.name}</h2>
          <StatusIndicator compact status={a.state==='completed'?'idle':a.state==='unknown'?'warning':'running'} label={labels[a.state]} />
          <Badge variant="outline" size="sm">版本 {a.version}</Badge>
        </div>
        <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[11px] text-muted-foreground">
          <span>{a.id}</span><a href={`?view=state&intent=${encodeURIComponent(a.intent_id)}`} className="inline-flex items-center gap-1 underline underline-offset-2">{a.intent_id}<ArrowUpRight className="h-3 w-3" /></a><a href={`?view=runs&run=${encodeURIComponent(a.run_id)}`} className="underline underline-offset-2">{a.run_id}</a><span>{fmtTime(a.updated_at)}</span>
        </div>
        {a.reference && <div className="mt-2 break-all text-xs text-muted-foreground">外部引用：{a.reference}</div>}
        {a.result && <div className="mt-3 whitespace-pre-wrap break-words rounded border border-border bg-muted/30 p-3 text-xs">{a.result}</div>}
        {a.result_ref && <div className="mt-2 break-all text-xs text-muted-foreground">结果引用：{a.result_ref}</div>}
        {editing?.id===a.id ? <div className="mt-3 space-y-2 rounded border border-border p-3">
          <label className="block text-xs">记录状态<select aria-label="动作结果状态" className="ml-2 rounded border border-border bg-background p-1" value={state} onChange={e=>setState(e.target.value as 'completed'|'unknown')} disabled={busy}><option value="completed">已取得结果</option><option value="unknown">结果尚不确定</option></select></label>
          <Textarea aria-label="动作结果或未知原因" placeholder="填写已核对的结果，或说明为何尚不确定" value={result} onChange={e=>setResult(e.target.value)} disabled={busy} />
          <Input aria-label="动作结果引用" placeholder="结果引用（可选）" value={reference} onChange={e=>setReference(e.target.value)} disabled={busy} />
          <div className="flex gap-2"><Button size="sm" disabled={busy || !result.trim()} onClick={()=>void save()}>{busy?'提交中…':'记录结果'}</Button><Button size="sm" variant="ghost" disabled={busy} onClick={()=>setEditing(undefined)}>取消</Button></div>
          <p className="text-[11px] text-muted-foreground">提交版本 {editing.version}。若版本已变化，请取消后重新核对最新记录。</p>
        </div> : a.state!=='completed' && <Button className="mt-3" size="sm" variant="outline" onClick={()=>{setEditing(a);setResult('');setReference('');setState('completed');setError('')}}>核对并记录结果</Button>}
      </article>)}
    </div>
  </Page>
}
