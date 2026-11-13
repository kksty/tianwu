// 状态 group (docs/34 §1.2): 执行者 (ProjectState) / 焦点 (FocusPanel) share one
// page. ProjectState keeps its own <Page> header; FocusPanel is a panel without
// a page frame, so the focus tab supplies the same 焦点 header around it.

import { TabBar, type TabItem } from '@/components/TabBar'
import { FocusPanel } from '@/components/FocusPanel'
import { Page } from '@/components/Page'
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
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <TabBar tabs={STATE_TABS} tab={tab} onTab={onTab} ariaLabel="状态" />
      <div className="min-h-0 flex-1">
        {tab === 'state' ? (
          <ProjectState
            onNavigate={onNavigate}
            onOpenIntent={onOpenIntent}
            onOpenRun={onOpenRun}
          />
        ) : (
          <Page title="焦点" scroll={false}>
            <FocusPanel onOpenFact={onOpenFact} />
          </Page>
        )}
      </div>
    </div>
  )
}
