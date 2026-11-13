import { useState } from 'react'
import { Boxes, Download, Loader2, Network } from 'lucide-react'
import { Badge, Button, Callout, Chip, EmptyState, Field, Input, SkeletonList } from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'

/**
 * Assets are a two-column reading page: the host/service inventory on the left
 * (one hairline row per host, ports as inset chips) and the import form on the
 * right as the page's single primary action. The list is the subject; the form
 * is a tool that sits beside it.
 */
export function Assets() {
  const list = useLoad(api.assets, [])
  const [path, setPath] = useState('')
  const [kind, setKind] = useState<'assets' | 'traffic'>('assets')
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')

  const hosts = list.data ?? []
  const services = hosts.reduce((n, h) => n + h.ports.length, 0)

  async function runImport() {
    if (!path.trim()) {
      setNote('需要服务器上的文件路径')
      return
    }
    setBusy(true)
    setNote('')
    try {
      const res = kind === 'assets' ? await api.importAssets(path.trim()) : await api.importTraffic(path.trim())
      setNote(JSON.stringify(res))
      setPath('')
      list.reload()
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Page
      title="资产"
      subtitle={`${hosts.length} 主机 · ${services} 个 ip:port —— 资产是控制台的一页，报告仍是漏洞快照`}
      actions={
        <Button size="sm" variant="outline" onClick={() => list.reload()} disabled={list.loading || list.refreshing}>
          <Network className="h-3.5 w-3.5" /> 刷新
        </Button>
      }
    >
      <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-3">
        <div className="min-w-0 space-y-3 xl:col-span-2">
          {list.error && (
            <Callout tone="destructive">
              {list.error}
              <Button size="xs" variant="outline" className="ml-2" onClick={() => list.reload()}>
                重试
              </Button>
            </Callout>
          )}

          <Panel
            title="主机与端口"
            extra={
              <span className="tw-nums font-mono text-[11px] text-ink-3">
                {hosts.length} 主机 · {services} 端口
              </span>
            }
            bodyClassName="p-0"
          >
            {list.loading && (
              <div className="p-4">
                <SkeletonList count={4} />
              </div>
            )}

            {!list.loading && hosts.length === 0 && (
              <EmptyState
                icon={Boxes}
                title="还没有资产"
                description="导入 harness 的扫描结果（gogo / neutron JSONL），每个 ip:port 落一条 service 事实。"
              />
            )}

            {hosts.length > 0 && (
              <div className="divide-y divide-hairline">
                {hosts.map((h) => (
                  <div key={h.host} className="px-3 py-2.5 transition-colors hover:bg-surface-2">
                    <div className="flex min-h-[24px] flex-wrap items-center gap-2">
                      <Network className="h-3.5 w-3.5 shrink-0 text-ink-3" />
                      <span className="tw-break font-mono text-[13px] font-medium leading-5 text-ink">
                        {h.host}
                      </span>
                      <span className="tw-nums ml-auto shrink-0 font-mono text-[11px] text-ink-3">
                        {h.ports.length} 端口
                      </span>
                    </div>
                    {h.ports.length === 0 ? (
                      <p className="mt-1 text-[12px] leading-relaxed text-ink-2">只有主机事实，没有端口</p>
                    ) : (
                      <div className="mt-2 flex flex-wrap gap-1.5">
                        {h.ports.map((p) => (
                          <span
                            key={p.port}
                            title={p.summary || p.port}
                            className="tw-inset flex max-w-full items-center gap-1.5 rounded px-1.5 py-1"
                          >
                            <span className="tw-nums shrink-0 font-mono text-[11px] font-medium text-ink-2">
                              {p.port}
                            </span>
                            {p.summary && (
                              <span className="max-w-[220px] truncate text-[11px] leading-4 text-ink-3">
                                {p.summary}
                              </span>
                            )}
                          </span>
                        ))}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
          </Panel>
        </div>

        <Panel title="导入" extra={<Badge size="sm" variant="muted">服务器文件</Badge>}>
          <div className="space-y-3">
            <div className="flex flex-wrap gap-1.5" role="group" aria-label="导入类型">
              <Chip active={kind === 'assets'} onClick={() => setKind('assets')}>
                扫描结果
              </Chip>
              <Chip active={kind === 'traffic'} onClick={() => setKind('traffic')}>
                HAR 流量
              </Chip>
            </div>
            <Field
              label="文件路径"
              hint={
                kind === 'assets'
                  ? '相对案例目录，或绝对路径；吃 gogo / neutron 的 JSONL'
                  : 'HAR 文件在服务器上的位置；录制交给浏览器 / burp / mitmproxy'
              }
            >
              <Input
                value={path}
                onChange={(e) => setPath(e.target.value)}
                placeholder={kind === 'assets' ? 'cases/xxx/scan.jsonl' : 'C:\\caps\\site.har'}
                className="h-8 font-mono text-[12px]"
                disabled={busy}
              />
            </Field>
            <Button size="sm" onClick={runImport} disabled={busy}>
              {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Download className="h-3.5 w-3.5" />}
              {busy ? '导入中…' : '导入'}
            </Button>
            {note && (
              <pre
                role="status"
                className="tw-inset tw-break whitespace-pre-wrap rounded-lg p-2.5 font-mono text-[11px] leading-relaxed text-ink-2"
              >
                {note}
              </pre>
            )}
          </div>
        </Panel>
      </div>
    </Page>
  )
}
