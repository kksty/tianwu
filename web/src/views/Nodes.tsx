import { Suspense, lazy, useState } from 'react'
import { FileSearch, Network, RefreshCw, SquareTerminal } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  SkeletonList,
  StatusIndicator,
} from '@cyber/ui'
import { Page } from '@/components/Page'
import { api } from '@/lib/api'
import { usePoll } from '@/lib/hooks'
import { hostPresentation } from '@/lib/nodes'

// Opening this page must not download xterm: the terminal pane is the only
// consumer of that ~330 KB chunk, and most visits never open one.
const NodeTerminal = lazy(() =>
  import('@/components/NodeTerminal').then((m) => ({ default: m.NodeTerminal })),
)

/**
 * Execution locations in the single-machine edition. The control plane starts
 * `aiscan web` itself and the project is bound to that host automatically, so
 * this page is a status view (host, endpoint, terminal), not a node picker.
 */
export function Nodes() {
  const list = usePoll(api.nodes, 4000)
  const nodes = list.data?.nodes ?? []
  const [terminal, setTerminal] = useState<string | null>(null)

  return (
    <Page
      title="执行"
      subtitle="单机实例：控制台启动的本机 aiscan web 就是执行节点，终端与 REPL 由它提供"
      scroll={false}
      actions={<Button variant="outline" size="sm" onClick={list.reload} disabled={list.loading || list.refreshing}><RefreshCw className="mr-1 h-3 w-3" />刷新状态</Button>}
    >
      <div className="flex h-full min-h-0 flex-col gap-3 overflow-y-auto lg:flex-row lg:overflow-hidden">
        <div className="min-w-0 flex-none space-y-2 py-1 pr-1 lg:flex-1 lg:overflow-y-auto">
          {list.error && <Callout tone="destructive" className="mb-2">{list.error}</Callout>}
          {list.loading && <SkeletonList count={2} />}

          <div className="space-y-1.5">
            {nodes.map((n) => {
              const aop = n.transport === 'aop'
              const where = aop ? n.endpoint : n.bin
              const runtime = hostPresentation(n.host)
              return (
                <div
                  key={n.id}
                  className="flex items-start gap-2 rounded-md border border-border bg-card px-3 py-2"
                >
                  {aop ? (
                    <Network className="mt-1 h-4 w-4 shrink-0 text-muted-foreground" />
                  ) : (
                    <FileSearch className="mt-1 h-4 w-4 shrink-0 text-muted-foreground" />
                  )}
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-mono text-ui text-foreground">{n.id}</span>
                      {n.default && <Badge size="sm" variant="secondary">实例默认</Badge>}
                      {n.project_default && <Badge size="sm" variant="primary">项目默认</Badge>}
                      <StatusIndicator
                        compact
                        status={aop ? runtime.status : 'idle'}
                        label={aop ? runtime.label : '按需启动审计进程'}
                      />
                    </div>
                    {n.note && <div className="text-[12px] text-muted-foreground">{n.note}</div>}
                    {where && (
                      <div className="tw-break font-mono text-[11px] text-muted-foreground">{where}</div>
                    )}
                    {n.host && <div className="mt-1 text-[11px] text-muted-foreground">{n.host.pid > 0 ? `PID ${n.host.pid} · ` : ''}重启 {n.host.restarts} 次</div>}
                    {n.host && !n.host.ready && (
                      <Callout tone="destructive" className="mt-2">
                        <div className="tw-break whitespace-pre-wrap text-[12px]">{n.host.last_error || '宿主正在启动，等待模型就绪。'}</div>
                        <div className="mt-1 text-[12px]">默认模型暂不能派发。检查模型账户、连接与配置后，<a href="/g/?view=settings" className="underline underline-offset-2">到设置 → 模型保存重试</a>；无需绑定节点。</div>
                      </Callout>
                    )}
                  </div>
                  {n.terminal && (
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-7 shrink-0 gap-1 text-[12px]"
                      onClick={() => setTerminal(n.id)}
                      disabled={aop && !runtime.terminal}
                    >
                      <SquareTerminal className="h-3 w-3" />
                      终端
                    </Button>
                  )}
                </div>
              )
            })}
          </div>

          <Callout tone="info" className="mt-3">
            <div className="space-y-1">
              <div>
                控制面启动时自己起一个 <span className="font-mono">aiscan web</span>，项目使用这个本机宿主；
                不需要 <span className="font-mono">nodes.json</span>，也不需要人工勾选节点。
              </div>
              <div>
                单机版忽略远端 AOP 条目；<span className="font-mono">nodes.json</span> 只保留
                <span className="font-mono"> audit: true </span>的本地一次性审计进程（cyber-audit）：
              </div>
              <pre className="tw-mono tw-break whitespace-pre-wrap rounded border border-border bg-muted/40 p-2 text-[11px] leading-relaxed">
{`{"nodes": [{"id": "audit", "bin": "../tools/cyber-audit.exe", "audit": true}]}`}
              </pre>
              <div>
                宿主的 cwd 是 <span className="font-mono">{"<data>"}</span>，所以它看得见
                <span className="font-mono"> projects/</span>；终端与 REPL 都跑在这个宿主里。
              </div>
            </div>
          </Callout>
        </div>

        {terminal && (
          <div className="h-[420px] min-w-0 flex-none pb-1 lg:h-auto lg:flex-[1.15]">
            <Suspense
              fallback={
                <div className="flex h-full items-center justify-center rounded-lg border border-border bg-card/40 text-[12px] text-muted-foreground">
                  终端内核加载中…
                </div>
              }
            >
              <NodeTerminal node={terminal} onClose={() => setTerminal(null)} />
            </Suspense>
          </div>
        )}
      </div>
    </Page>
  )
}
