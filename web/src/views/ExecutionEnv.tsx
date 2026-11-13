// 执行环境 group (docs/34 §1.2): 节点 / 终端 / REPL / 工具与能力 share one page.
// A container, not a rewrite: each child keeps its own <Page> header and logic.

import { TabBar, type TabItem } from '@/components/TabBar'
import { Nodes } from './Nodes'
import { ReplView, TerminalView } from './Terminal'
import { Toolbox } from './Toolbox'

export type RuntimeTab = 'nodes' | 'terminal' | 'repl' | 'toolbox'

const RUNTIME_TABS: readonly TabItem<RuntimeTab>[] = [
  { id: 'nodes', label: '节点' },
  { id: 'terminal', label: '终端' },
  { id: 'repl', label: 'REPL' },
  { id: 'toolbox', label: '工具与能力' },
]

export function ExecutionEnv({
  tab,
  onTab,
}: {
  tab: RuntimeTab
  onTab: (tab: RuntimeTab) => void
}) {
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col bg-background">
      <TabBar tabs={RUNTIME_TABS} tab={tab} onTab={onTab} ariaLabel="执行环境" />
      <div className="min-h-0 min-w-0 flex-1 overflow-hidden">
        {tab === 'nodes' ? (
          <Nodes />
        ) : tab === 'terminal' ? (
          <TerminalView />
        ) : tab === 'repl' ? (
          <ReplView />
        ) : (
          <Toolbox />
        )}
      </div>
    </div>
  )
}
