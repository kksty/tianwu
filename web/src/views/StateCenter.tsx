// 状态 group (docs/34 §1.2): 执行者 (ProjectState) / 焦点 (FocusPanel) share one
// page. ProjectState keeps its own <Page> header; the focus tab gets the same
// Page frame plus a titled Panel so both tabs read as the same kind of page.

import { TabBar, type TabItem } from '@/components/TabBar'
import { FocusPanel } from '@/components/FocusPanel'
import { Page, Panel } from '@/components/Page'
import { ProjectState } from './ProjectState'

export type StateTab = 'state' | 'focus'

const STATE_TABS: readonly TabItem<StateTab>[] = [
  { id: 'state', label: '执行者' },
  { id: 'focus', label: '焦点' },
]

export function StateCenter({
  tab,
  onTab,
  onNavigate,
  onOpenIntent,
  onOpenRun,
  onOpenFact,
}: {
  tab: StateTab
  onTab: (t: StateTab) => void
  onNavigate?: (view: string) => void
  /** ProjectState: open the direction in the graph. */
  onOpenIntent: (intentID: string) => void
  /** ProjectState: open the run in the run ledger. */
  onOpenRun: (runID: string) => void
  /** FocusPanel: open the fact on the graph. */
  onOpenFact: (factID: string) => void
}) {
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col bg-background">
      <TabBar tabs={STATE_TABS} tab={tab} onTab={onTab} ariaLabel="状态" />
      <div className="min-h-0 min-w-0 flex-1 overflow-hidden">
        {tab === 'state' ? (
          <ProjectState
            onNavigate={onNavigate}
            onOpenIntent={onOpenIntent}
            onOpenRun={onOpenRun}
          />
        ) : (
          <Page
            title="焦点"
            subtitle="热/温/冷三层焦点索引；截断或未列出不等于事实不存在，只读、不是新结论"
            scroll={false}
          >
            <div className="h-full min-h-0 p-4 sm:p-5 xl:p-6">
              <Panel
                title="焦点投影"
                className="flex h-full min-h-0 flex-col"
                bodyClassName="flex min-h-0 flex-1 flex-col p-0"
              >
                <FocusPanel onOpenFact={onOpenFact} />
              </Panel>
            </div>
          </Page>
        )}
      </div>
    </div>
  )
}
