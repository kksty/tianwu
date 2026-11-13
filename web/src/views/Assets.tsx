import { useState } from 'react'
import { Boxes, Download, Network } from 'lucide-react'
import { Badge, Button, Callout, EmptyState, Field, Input, SkeletonList } from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'

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
        <Button size="sm" variant="outline" onClick={() => list.reload()}>
          <Network className="h-3.5 w-3.5" /> 刷新
        </Button>
      }
    >
      <div className="grid grid-cols-1 gap-3 xl:grid-cols-3">
        <div className="space-y-2 xl:col-span-2">
          {list.error && <Callout tone="destructive">{list.error}</Callout>}
          {list.loading && <SkeletonList count={4} />}
          {!list.loading && hosts.length === 0 && (
            <EmptyState
              icon={Boxes}
              title="还没有资产"
              description="导入 harness 的扫描结果（gogo / neutron JSONL），每个 ip:port 落一条 service 事实。"
            />
          )}
          {hosts.map((h) => (
            <div key={h.host} className="rounded-md border border-border bg-card">
              <div className="flex items-center gap-2 border-b border-border px-3 py-1.5">
                <span className="tw-mono text-ui text-foreground">{h.host}</span>
                <Badge size="sm" variant="muted" className="ml-auto">
                  {h.ports.length} 端口
                </Badge>
              </div>
              <div className="flex flex-wrap gap-1.5 p-2">
                {h.ports.length === 0 && (
                  <span className="text-[12px] text-muted-foreground">只有主机事实，没有端口</span>
                )}
                {h.ports.map((p) => (
                  <span
                    key={p.port}
                    title={p.summary}
                    className="flex max-w-full items-center gap-1.5 rounded border border-border bg-muted/40 px-1.5 py-0.5 text-[12px]"
                  >
                    <span className="font-mono text-foreground">{p.port}</span>
                    {p.summary && (
                      <span className="max-w-[220px] truncate text-muted-foreground">{p.summary}</span>
                    )}
                  </span>
                ))}
              </div>
            </div>
          ))}
        </div>

        <Panel title="导入">
          <div className="space-y-2">
            <div className="flex gap-1">
              <Button size="xs" variant={kind === 'assets' ? 'soft' : 'ghost'} onClick={() => setKind('assets')}>
                扫描结果
              </Button>
              <Button size="xs" variant={kind === 'traffic' ? 'soft' : 'ghost'} onClick={() => setKind('traffic')}>
                HAR 流量
              </Button>
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
                className="font-mono text-[12px]"
              />
            </Field>
            <Button size="sm" onClick={runImport} disabled={busy}>
              <Download className="h-3 w-3" /> {busy ? '导入中…' : '导入'}
            </Button>
            {note && <pre className="tw-break whitespace-pre-wrap text-[11px] text-muted-foreground">{note}</pre>}
          </div>
        </Panel>
      </div>
    </Page>
  )
}
