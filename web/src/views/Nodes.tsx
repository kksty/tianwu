import { Suspense, lazy, useState } from 'react'
import { FileSearch, Network, RefreshCw, SquareTerminal } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  EmptyState,
  SkeletonList,
  StatusIndicator,
} from '@cyber/ui'
import { cn } from '@cyber/theme'
import { Page, Panel } from '@/components/Page'
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
 *
 * One row per node: mono id + state badges, host facts as quiet mono meta, and
 * the terminal as the row's only action. Opening a terminal marks the row with
 * the 2px accent so the pane on the right has a visible owner.
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
      actions={
        <Button variant="outline" size="sm" onClick={list.reload} disabled={list.loading || list.refreshing}>
          <RefreshCw className={cn('h-3.5 w-3.5', list.refreshing && 'animate-spin')} /> 刷新状态
        </Button>
      }
    >
      <div className="flex h-full min-h-0 flex-col gap-3 overflow-y-auto lg:flex-row lg:overflow-hidden">
        <div className="min-w-0 flex-none space-y-4 py-1 pr-1 lg:flex-1 lg:overflow-y-auto">
          {list.error && (
            <Callout tone="destructive">
              {list.error}
              <Button size="xs" variant="outline" className="ml-2" onClick={list.reload}>
                重试
              </Button>
            </Callout>
          )}

          {list.loading && (
            <Panel bodyClassName="p-4">
              <SkeletonList count={2} />
            </Panel>
          )}

          {!list.loading && nodes.length === 0 && (
            <Panel bodyClassName="p-0">
              <EmptyState
                icon={Network}
                title="没有执行节点"
                description="单机版控制台启动的本机 aiscan web 就是执行节点；这里为空说明本机宿主没起来，可看控制台日志里的「host:」。"
              />
            </Panel>
          )}

          {nodes.length > 0 && (
            <Panel
              title="执行节点"
              extra={
                <span className="tw-nums font-mono text-[11px] text-ink-3">{nodes.length} 个</span>
              }
              bodyClassName="p-0"
            >
              <div className="divide-y divide-hairline">
                {nodes.map((n) => {
                  const aop = n.transport === 'aop'
                  const where = aop ? n.endpoint : n.bin
                  const runtime = hostPresentation(n.host)
                  const active = terminal === n.id
                  return (
                    <div
                      key={n.id}
                      className={cn(
                        'px-2.5 py-2.5 transition-colors',
                        active
                          ? 'bg-primary/[0.09] shadow-[inset_2px_0_0_var(--primary)]'
                          : 'hover:bg-surface-2',
                      )}
                    >
                      <div className="flex items-start gap-2.5">
                        {aop ? (
                          <Network className="mt-1 h-4 w-4 shrink-0 text-ink-3" />
                        ) : (
                          <FileSearch className="mt-1 h-4 w-4 shrink-0 text-ink-3" />
                        )}
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-center gap-2">
                            {n.terminal ? (
                              <button
                                type="button"
                                onClick={() => setTerminal(n.id)}
                                disabled={aop && !runtime.terminal}
                                aria-expanded={active}
                                title="在下方面板打开终端"
                                className="tw-break rounded text-left font-mono text-[13px] font-medium leading-5 text-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-40"
                              >
                                {n.id}
                              </button>
                            ) : (
                              <span className="tw-break font-mono text-[13px] font-medium leading-5 text-ink">{n.id}</span>
                            )}
                            {n.default && <Badge size="sm" variant="secondary">实例默认</Badge>}
                            {n.project_default && <Badge size="sm" variant="primary">项目默认</Badge>}
                            <StatusIndicator
                              compact
                              status={aop ? runtime.status : 'idle'}
                              label={aop ? runtime.label : '按需启动审计进程'}
                            />
                          </div>
                          {n.note && (
                            <div className="tw-break mt-1 text-[12px] leading-relaxed text-ink-2">{n.note}</div>
                          )}
                          {where && (
                            <div className="tw-break mt-1 font-mono text-[11px] leading-4 text-ink-3">{where}</div>
                          )}
                          {n.host && (
                            <div className="mt-0.5 flex flex-wrap gap-x-3 text-[11px] leading-4 text-ink-3">
                              {n.host.pid > 0 && (
                                <span className="tw-nums font-mono">PID {n.host.pid}</span>
                              )}
                              <span>重启 <span className="tw-nums font-mono">{n.host.restarts}</span> 次</span>
                            </div>
                          )}
                          {n.host && !n.host.ready && (
                            <Callout tone="destructive" className="mt-2">
                              <div className="tw-break whitespace-pre-wrap text-[12px]">{n.host.last_error || '宿主正在启动，等待模型就绪。'}</div>
                              <div className="mt-1 text-[12px]">
                                默认模型暂不能派发。检查模型账户、连接与配置后，<a href="/g/?view=settings" className="underline underline-offset-2">到设置 → 模型保存重试</a>；无需绑定节点。
                              </div>
                            </Callout>
                          )}
                        </div>
                        {n.terminal && (
                          <Button
                            variant={active ? 'soft' : 'outline'}
                            size="xs"
                            className="shrink-0"
                            onClick={() => setTerminal(n.id)}
                            disabled={aop && !runtime.terminal}
                            active={active}
                            aria-label={`打开 ${n.id} 的终端`}
                          >
                            <SquareTerminal className="h-3 w-3" />
                            终端
                          </Button>
                        )}
                      </div>
                    </div>
                  )
                })}
              </div>
            </Panel>
          )}

          <Callout tone="info">
            <div className="space-y-1.5">
              <div>
                控制面启动时自己起一个 <span className="font-mono">aiscan web</span>，项目使用这个本机宿主；
                不需要 <span className="font-mono">nodes.json</span>，也不需要人工勾选节点。
              </div>
              <div>
                单机版忽略远端 AOP 条目；<span className="font-mono">nodes.json</span> 只保留
                <span className="font-mono"> audit: true </span>的本地一次性审计进程（cyber-audit）：
              </div>
              <pre className="tw-mono tw-break tw-inset whitespace-pre-wrap rounded-lg p-2.5 text-[11px] leading-relaxed text-ink-2">
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
          <div className="flex h-[420px] min-w-0 flex-none flex-col gap-2 pb-1 lg:h-auto lg:flex-[1.15]">
            <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl">
              <Suspense
                fallback={
                  <div className="tw-panel flex h-full min-h-[200px] flex-col gap-3 rounded-xl p-4">
                    <span className="text-[12px] text-ink-2">终端内核加载中…</span>
                    <SkeletonList count={4} />
                  </div>
                }
              >
                <NodeTerminal node={terminal} onClose={() => setTerminal(null)} />
              </Suspense>
            </div>
            <p className="tw-break px-1 font-mono text-[11px] leading-4 text-ink-3">
              终端跑在本机 aiscan web 宿主里；关闭面板不会停止宿主。
            </p>
          </div>
        )}
      </div>
    </Page>
  )
}
