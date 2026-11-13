import { useEffect, useState } from 'react'
import { Download, Network, RefreshCw, Search } from 'lucide-react'
import { Badge, Button, Callout, EmptyState, Input, SkeletonList } from '@cyber/ui'
import { cn } from '@cyber/theme'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtTime } from '@/lib/format'
import { useDebounced, useLoad, usePoll } from '@/lib/hooks'
import { bodyPreview } from '@/lib/traffic'
import type { HTTPFlow } from '@/lib/types'

/**
 * The captured-traffic reader: a scannable request list on the left, the
 * selected exchange on the right. The list row shows method / status / time /
 * URL; the detail separates request and response into their own sections with
 * mono packet text and an explicit body preview, so a 64 KiB body cannot push
 * the layout sideways.
 */
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
  const pageIndex = Math.floor(offset / 100) + 1
  return (
    <Page
      title="HTTP 流量"
      subtitle="本项目执行轮观察到的请求与响应 · 自动刷新，不触发模型调用"
      scroll={false}
      actions={
        <Button size="sm" variant="outline" onClick={() => { list.reload(); detail.reload() }} disabled={list.loading || detail.loading}>
          <RefreshCw className={cn('h-3.5 w-3.5', (list.refreshing || detail.refreshing) && 'animate-spin')} /> 刷新
        </Button>
      }
    >
      <div className="flex h-full min-h-0 flex-col">
        <div className="shrink-0 space-y-2 border-b border-hairline p-3 sm:px-5">
          <p className="text-[12px] leading-relaxed text-ink-3">
            记录经过 harness 出口且已开启捕获的 HTTP／HTTPS。HTTPS 明文需要 MITM 与证书信任；绕过代理的程序、隧道内部和其他 TCP 流量不一定可见。
          </p>
          <div className="relative max-w-xl">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" />
            <Input
              aria-label="筛选 HTTP 流量"
              value={q}
              onChange={e => setQ(e.target.value)}
              placeholder="筛选 URL、方法、状态码或方向 ID"
              className="h-8 pl-8 text-[12px]"
            />
          </div>
        </div>

        <div className="flex min-h-0 flex-1 flex-col xl:flex-row">
          <aside className="flex max-h-[45%] min-h-0 flex-col border-b border-hairline xl:max-h-none xl:w-[44%] xl:border-b-0 xl:border-r">
            <div className="min-h-0 flex-1 overflow-auto">
              {(list.loading || (!page && list.refreshing)) && (
                <div className="p-3">
                  <SkeletonList count={6} />
                </div>
              )}
              {list.error && (
                <div className="p-3">
                  <Callout tone="destructive">
                    {list.error}
                    <Button size="xs" variant="outline" className="ml-2" onClick={list.reload}>
                      重试
                    </Button>
                  </Callout>
                </div>
              )}
              {rows.length > 0 && (
                <div className="divide-y divide-hairline">
                  {rows.map(row => {
                    const active = row.ref === ref
                    const bytes = row.response_bytes.toLocaleString()
                    return (
                      <button
                        key={row.ref}
                        type="button"
                        onClick={() => setSelected(row.ref)}
                        aria-pressed={active}
                        className={cn(
                          'block w-full px-2.5 py-2.5 text-left transition-colors',
                          active
                            ? 'bg-primary/[0.09] shadow-[inset_2px_0_0_var(--primary)]'
                            : 'hover:bg-surface-2',
                        )}
                      >
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-mono text-[11px] font-semibold leading-4 text-ink">{row.method}</span>
                          <Badge variant={row.status >= 500 || row.error ? 'warning' : 'muted'} size="sm">
                            <span className="tw-nums font-mono">{row.status || '无响应'}</span>
                          </Badge>
                          <time className="tw-nums ml-auto whitespace-nowrap font-mono text-[11px] leading-4 text-ink-3">{fmtTime(row.at)}</time>
                        </div>
                        <p className="tw-break mt-1 font-mono text-[12px] leading-5 text-ink">{row.url}</p>
                        <p className="tw-break mt-0.5 font-mono text-[11px] leading-4 text-ink-3">
                          {row.run_id} · 响应 <span className="tw-nums">{bytes}</span> B{!row.complete ? ' · 记录不完整' : ''}
                        </p>
                      </button>
                    )
                  })}
                </div>
              )}
              {!list.loading && !list.error && rows.length === 0 && (
                <EmptyState
                  icon={Network}
                  title="暂无已捕获流量"
                  description="执行者产生可观测 HTTP 请求后，会出现在这里。已有旧运行不会自动补采。"
                />
              )}
            </div>
            <div className="flex items-center gap-2 border-t border-hairline p-2.5 text-[11px] text-ink-3">
              <span className="tw-nums font-mono">共 {page?.total ?? 0} 条 · 第 {pageIndex} 页</span>
              <Button size="xs" variant="ghost" className="ml-auto" disabled={offset === 0} onClick={() => { setSelected(''); setOffset(Math.max(0, offset - 100)) }}>
                上一页
              </Button>
              <Button size="xs" variant="ghost" disabled={offset + 100 >= (page?.total ?? 0)} onClick={() => { setSelected(''); setOffset(offset + 100) }}>
                下一页
              </Button>
            </div>
          </aside>

          <section className="min-h-0 min-w-0 flex-1 overflow-auto p-4 sm:p-5">
            {detail.loading && <SkeletonList count={4} />}
            {detail.error && (
              <Callout tone="destructive">
                {detail.error}
                <Button size="xs" variant="outline" className="ml-2" onClick={detail.reload}>
                  重试
                </Button>
              </Callout>
            )}
            {!detail.loading && !current && !detail.error && rows.length > 0 && (
              <EmptyState compact icon={Network} title="选择一条流量" description="左侧选择一条请求，查看请求头、响应头与正文预览。" />
            )}
            {current && (
              <div className="space-y-4">
                {page && selected && !rows.some(row => row.ref === selected) && (
                  <Callout>当前选中记录已移出本页列表，详情保持不变。</Callout>
                )}
                <div className="flex flex-wrap items-center gap-2">
                  <h2 className="tw-section-title">请求与响应</h2>
                  <a
                    href={api.httpFlowURL(current.record.ref)}
                    className="ml-auto inline-flex items-center gap-1 rounded text-[12px] text-ink-2 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                  >
                    <Download className="h-3.5 w-3.5" /> 下载原始记录
                  </a>
                </div>
                <dl className="tw-inset grid grid-cols-1 gap-x-4 gap-y-1.5 rounded-lg p-3 font-mono text-[11px] leading-5 text-ink-3 sm:grid-cols-2">
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>运行</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.run_id}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>方向</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.intent_id || '—'}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>会话</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.session_id || '未提供'}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>流量</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.flow_id}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>操作</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.operation?.operation_id || '未提供'}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>调用</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.operation?.call_id || '未提供'}</dd></div>
                </dl>
                {current.record.error && <Callout tone="warning">{current.record.error}</Callout>}
                {!current.record.complete && <Callout tone="warning">原生记录标记为不完整，正文可能缺失。</Callout>}
                <Exchange
                  title="请求"
                  start={`${current.flow.request?.method ?? ''} ${current.flow.request?.url ?? ''} ${current.flow.request?.protocol ?? ''}`}
                  headers={current.flow.request?.headers}
                  body={current.flow.request?.body}
                />
                {current.flow.response ? (
                  <Exchange
                    title="响应"
                    start={`${current.flow.response.status_code ?? ''} ${current.flow.response.reason_phrase ?? ''}`}
                    headers={current.flow.response.headers}
                    body={current.flow.response.body}
                  />
                ) : (
                  <Callout>未收到响应；不推断为成功或漏洞。</Callout>
                )}
              </div>
            )}
          </section>
        </div>
      </div>
    </Page>
  )
}

function Exchange({title, start, headers = [], body = ''}: {title: string; start: string; headers?: {name: string; value: string}[]; body?: string}) {
  const preview = bodyPreview(body)
  return (
    <Panel title={title} bodyClassName="space-y-2">
      <pre className="tw-inset tw-break overflow-auto whitespace-pre-wrap rounded-lg p-3 font-mono text-[11px] leading-5 text-ink-2">
        {start}{'\n'}{headers.map(h => `${h.name}: ${h.value}`).join('\n')}
      </pre>
      <pre className="tw-inset tw-break max-h-[420px] overflow-auto whitespace-pre-wrap rounded-lg p-3 font-mono text-[11px] leading-5 text-ink-2">
        {preview.text || '（空正文）'}
      </pre>
      <p className="tw-nums text-[11px] leading-4 text-ink-3">
        {preview.size.toLocaleString()} B
        {preview.binary ? ' · 二进制，十六进制预览' : ''}
        {preview.truncated ? ' · 仅预览前 64 KiB，可下载完整记录' : ''}
      </p>
    </Panel>
  )
}
