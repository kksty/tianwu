import { useEffect, useState } from 'react'
import { Download, Network, RefreshCw, Search } from 'lucide-react'
import { Badge, Button, Callout, EmptyState, Input, SkeletonList } from '@cyber/ui'
import { Page } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtTime } from '@/lib/format'
import { useDebounced, useLoad, usePoll } from '@/lib/hooks'
import { bodyPreview } from '@/lib/traffic'
import type { HTTPFlow } from '@/lib/types'

export function Traffic() {
  const [q, setQ] = useState('')
  const query = useDebounced(q, 200)
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState('')
  const list = usePoll(async () => ({...await api.httpFlows(query, offset), query, offset}), 5000, [query, offset])
  useEffect(() => { setOffset(0); setSelected('') }, [query])
  const page = list.data?.query === query && list.data.offset === offset ? list.data : undefined
  const rows = page?.flows ?? []
  const first = rows[0]?.ref ?? ''
  useEffect(() => { if (!selected && first) setSelected(first) }, [selected, first])
  const ref = selected || first
  const detail = useLoad(() => ref ? api.httpFlow(ref) : Promise.resolve<HTTPFlow | null>(null), [ref])
  // While a different detail is loading, never label the previous packet with
  // the new selection's identity.
  const current = detail.data?.record.ref === ref ? detail.data : null
  return <Page title="HTTP 流量" subtitle="本项目执行轮观察到的请求与响应 · 自动刷新，不触发模型调用" actions={<Button size="xs" variant="outline" onClick={() => {list.reload(); detail.reload()}}><RefreshCw className="h-3 w-3" />刷新</Button>} scroll={false}>
    <div className="flex h-full min-h-0 flex-col">
      <div className="shrink-0 space-y-3 border-b border-border p-3 sm:px-5">
        <p className="text-xs leading-5 text-muted-foreground">记录经过 harness 出口且已开启捕获的 HTTP／HTTPS。HTTPS 明文需要 MITM 与证书信任；绕过代理的程序、隧道内部和其他 TCP 流量不一定可见。</p>
        <div className="relative max-w-xl"><Search className="absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" /><Input aria-label="筛选 HTTP 流量" value={q} onChange={e => setQ(e.target.value)} placeholder="筛选 URL、方法、状态码或方向 ID" className="h-9 pl-8 text-xs" /></div>
      </div>
      <div className="flex min-h-0 flex-1 flex-col xl:flex-row">
        <aside className="flex max-h-[40%] min-h-0 flex-col border-b border-border xl:max-h-none xl:w-[45%] xl:border-b-0 xl:border-r">
          <div className="min-h-0 flex-1 overflow-auto p-2">
            {(list.loading || (!page && list.refreshing)) && <SkeletonList count={6} />}{list.error && <Callout tone="destructive">{list.error}</Callout>}
            {rows.map(row => <button key={row.ref} onClick={() => setSelected(row.ref)} className={`mb-1 block w-full rounded-lg p-3 text-left ${row.ref === ref ? 'bg-primary/10' : 'hover:bg-muted/60'}`}>
              <div className="flex items-center gap-2"><span className="font-mono text-[11px] font-semibold text-primary">{row.method}</span><Badge variant={row.status >= 500 || row.error ? 'warning' : 'muted'} size="sm">{row.status || '无响应'}</Badge><time className="ml-auto text-[10px] text-muted-foreground">{fmtTime(row.at)}</time></div>
              <p className="mt-1.5 break-all font-mono text-xs leading-5">{row.url}</p><p className="mt-1 text-[10px] text-muted-foreground">{row.run_id} · 响应 {row.response_bytes.toLocaleString()} B{!row.complete ? ' · 记录不完整' : ''}</p>
            </button>)}
            {!list.loading && !list.error && rows.length === 0 && <EmptyState icon={Network} title="暂无已捕获流量" description="执行者产生可观测 HTTP 请求后，会出现在这里。已有旧运行不会自动补采。" />}
          </div>
          <div className="flex items-center gap-2 border-t border-border p-3 text-xs text-muted-foreground"><span>共 {page?.total ?? 0} 条 · 第 {Math.floor(offset / 100) + 1} 页</span><Button size="xs" variant="ghost" className="ml-auto" disabled={offset === 0} onClick={() => {setSelected(''); setOffset(Math.max(0, offset - 100))}}>上一页</Button><Button size="xs" variant="ghost" disabled={offset + 100 >= (page?.total ?? 0)} onClick={() => {setSelected(''); setOffset(offset + 100)}}>下一页</Button></div>
        </aside>
        <section className="min-h-0 min-w-0 flex-1 overflow-auto p-4 sm:p-5">
          {detail.loading && <SkeletonList count={4} />}{detail.error && <Callout tone="destructive">{detail.error}</Callout>}
          {current && <div className="space-y-5">
            {page && selected && !rows.some(row => row.ref === selected) && <Callout>当前选中记录已移出本页列表，详情保持不变。</Callout>}
            <div className="flex flex-wrap items-center gap-2"><h2 className="text-sm font-semibold">请求与响应</h2><a href={api.httpFlowURL(current.record.ref)} className="ml-auto inline-flex items-center gap-1 text-xs text-primary"><Download className="h-3 w-3" />下载原始记录</a></div>
            <div className="break-all rounded-lg border border-border bg-muted/20 p-3 text-[11px] leading-6 text-muted-foreground"><p>运行：{current.record.run_id} · 方向：{current.record.intent_id || '—'}</p><p>会话：{current.record.session_id || '未提供'} · 流量：{current.record.flow_id}</p><p>操作：{current.record.operation?.operation_id || '未提供'} · 调用：{current.record.operation?.call_id || '未提供'}</p></div>
            {current.record.error && <Callout tone="warning">{current.record.error}</Callout>}
            {!current.record.complete && <Callout tone="warning">原生记录标记为不完整，正文可能缺失。</Callout>}
            <Exchange title="请求" start={`${current.flow.request?.method ?? ''} ${current.flow.request?.url ?? ''} ${current.flow.request?.protocol ?? ''}`} headers={current.flow.request?.headers} body={current.flow.request?.body} />
            {current.flow.response ? <Exchange title="响应" start={`${current.flow.response.status_code ?? ''} ${current.flow.response.reason_phrase ?? ''}`} headers={current.flow.response.headers} body={current.flow.response.body} /> : <Callout>未收到响应；不推断为成功或漏洞。</Callout>}
          </div>}
        </section>
      </div>
    </div>
  </Page>
}

function Exchange({title, start, headers = [], body = ''}: {title: string; start: string; headers?: {name: string; value: string}[]; body?: string}) {
  const preview = bodyPreview(body)
  return <section className="min-w-0 space-y-2"><h3 className="text-xs font-medium">{title}</h3><pre className="overflow-auto rounded-lg border border-border bg-muted/20 p-3 font-mono text-[11px] leading-6">{start}{'\n'}{headers.map(h => `${h.name}: ${h.value}`).join('\n')}</pre><pre className="max-h-[420px] overflow-auto whitespace-pre-wrap break-all rounded-lg border border-border p-3 font-mono text-[11px] leading-5">{preview.text || '（空正文）'}</pre><p className="text-[10px] text-muted-foreground">{preview.size.toLocaleString()} B{preview.binary ? ' · 二进制，十六进制预览' : ''}{preview.truncated ? ' · 仅预览前 64 KiB，可下载完整记录' : ''}</p></section>
}
