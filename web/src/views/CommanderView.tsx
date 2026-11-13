// 指挥 group (docs/34 §1.2): 决策 / 记忆 share one page. A container, not a
// rewrite: each child keeps its own <Page> header and logic, so the group only
// supplies the quiet tab strip and a stable scroll frame.

import { TabBar, type TabItem } from '@/components/TabBar'
import type { BoardResponse } from '@/lib/types'
import { Decisions } from './Decisions'
import { Memory } from './Memory'

export type CommanderTab = 'decisions' | 'memory'

const COMMANDER_TABS: readonly TabItem<CommanderTab>[] = [
  { id: 'decisions', label: '决策' },
  { id: 'memory', label: '记忆' },
]

export function CommanderView({
  tab,
  onTab,
  board,
}: {
  tab: CommanderTab
  onTab: (t: CommanderTab) => void
  board?: BoardResponse
}) {
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col bg-background">
      <TabBar tabs={COMMANDER_TABS} tab={tab} onTab={onTab} ariaLabel="指挥" />
      <div className="min-h-0 min-w-0 flex-1 overflow-hidden">
        {tab === 'decisions' ? <Decisions board={board} /> : <Memory />}
      </div>
    </div>
  )
}
