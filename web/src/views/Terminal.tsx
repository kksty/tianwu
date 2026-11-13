import { Suspense, lazy, useEffect, useState } from 'react'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@cyber/ui'
import { Page } from '@/components/Page'
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'

// xterm is the single biggest chunk in the console (~330 KB). It is fetched
// only when a terminal pane actually mounts, never because the page or the
// project took a refresh.
const NodeTerminal = lazy(() =>
  import('@/components/NodeTerminal').then((m) => ({ default: m.NodeTerminal })),
)

function TerminalFallback() {
  return (
    <div className="flex h-full min-h-[120px] items-center justify-center rounded-lg border border-border bg-card/40 text-[12px] text-muted-foreground">
      终端内核加载中…
    </div>
  )
}

/**
 * A full-height PTY on a node. Two entry points share it:
 * - 终端: the node's shell (current directory, tools, network checks);
 * - REPL: the harness host's interactive aiscan agent (`main-repl`).
 *
 * Both come from an AOP host, and every node is one: the control plane starts a
 * local `aiscan web` and lists it as "local" (D64). A node without `terminal`
 * is an audit node — a one-shot local binary with no PTY.
 */
function TerminalPage({ mode }: { mode: 'shell' | 'repl' }) {
  const list = useLoad(api.nodes, [])
  const nodes = (list.data?.nodes ?? []).filter((n) => n.terminal)
  const [node, setNode] = useState('')

  useEffect(() => {
    if (!list.data) return
    setNode((cur) => {
      if (nodes.some((n) => n.id === cur)) return cur
      return list.data?.project_default || nodes[0]?.id || ''
    })
  }, [list.data])

  const current = nodes.find((n) => n.id === node)
  const repl = mode === 'repl'

  return (
    <Page
      title={repl ? 'REPL' : '终端'}
      subtitle={
        repl
          ? '宿主上的 aiscan 交互式会话（main-repl）：手动追问、试命令、看它怎么想。同一宿主上的项目共享这一段对话'
          : '节点上的 shell（harness PTY）：验网段、看工具、应急处理'
      }
      scroll={false}
      actions={
        nodes.length > 1 ? (
          <Select value={node} onValueChange={setNode}>
            <SelectTrigger className="h-7 w-40 text-[12px]">
              <SelectValue placeholder="选择节点" />
            </SelectTrigger>
            <SelectContent>
              {nodes.map((n) => (
                <SelectItem key={n.id} value={n.id}>
                  {n.id}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : (
          <span className="font-mono text-[12px] text-muted-foreground">{node}</span>
        )
      }
    >
      <div className="flex h-full min-h-0 flex-col pb-1">
        {!node ? (
          <div className="p-4 text-[12px] text-muted-foreground">
            没有可开终端的节点。终端和 REPL 都由 harness 宿主提供：控制台启动时会自己起一个{' '}
            <span className="font-mono">aiscan web</span>（agent <span className="font-mono">local</span>），
            正常情况下这里至少有一个 local。为空说明本机宿主没起来——看控制台日志里的「host:」，
            或者这台只做控制面（<span className="font-mono">--no-local-host</span>）。
          </div>
        ) : (
          <div className="flex min-h-0 flex-1 flex-col">
            <Suspense fallback={<TerminalFallback />}>
              <NodeTerminal key={`${node}:${mode}`} node={node} mode={mode} />
            </Suspense>
            {current && (
              <div className="px-1 pt-1 text-[11px] text-muted-foreground">
                {`AOP 宿主 ${current.endpoint ?? ''} 上的 ${repl ? 'aiscan REPL' : 'shell'}`}
              </div>
            )}
          </div>
        )}
      </div>
    </Page>
  )
}

export function TerminalView() {
  return <TerminalPage mode="shell" />
}

export function ReplView() {
  return <TerminalPage mode="repl" />
}
