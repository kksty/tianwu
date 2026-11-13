// 运行 group (docs/34 §1.2): 运行记录 / 执行者 / 外部动作 share one page.
//
// A container, not a rewrite: each child keeps its own <Page> header and all of
// its logic. The active tab is shell state (`?view=runs&tab=workers`), so the
// component adds no local state — switching tabs remounts the child, which is
// how each child's own polls have always worked.

import { TabBar, type TabItem } from '@/components/TabBar'
import type { ProjectInfo } from '@/lib/types'
import { ExternalActions } from './ExternalActions'
import { Runs } from './Runs'
import { Workers } from './Workers'

export type RunTab = 'runs' | 'workers' | 'actions'

const RUN_TABS: readonly TabItem<RunTab>[] = [
  { id: 'runs', label: '运行记录' },
  { id: 'workers', label: '执行者' },
  { id: 'actions', label: '外部动作' },
]

export function RunCenter({
  tab,
  onTab,
  current,
  projects,
  onRefresh,
  onOpenIntent,
  onOpenGraphIntent,
  onOpenRun,
}: {
  tab: RunTab
  onTab: (tab: RunTab) => void
  current: string
  projects: ProjectInfo[]
  onRefresh: () => void
  /** → 状态页该方向（ProjectState 的 ?intent= 深链）。 */
  onOpenIntent: (intentID: string) => void
  /** → 图谱选中 s:<id>（T4 cross-links 的可选 prop，未落地前由页面忽略）。 */
  onOpenGraphIntent: (intentID: string) => void
  /** → 运行页该 run（T4 cross-links 的可选 prop）。 */
  onOpenRun?: (runID: string) => void
}) {
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <TabBar tabs={RUN_TABS} tab={tab} onTab={onTab} ariaLabel="运行" />
      <div className="min-h-0 flex-1">
        {tab === 'runs' ? (
          <Runs onOpenIntent={onOpenIntent} onOpenGraphIntent={onOpenGraphIntent} />
        ) : tab === 'workers' ? (
          <Workers
            current={current}
            projects={projects}
            onRefresh={onRefresh}
            onOpenGraphIntent={onOpenGraphIntent}
            onOpenRun={onOpenRun}
          />
        ) : (
          <ExternalActions />
        )}
      </div>
    </div>
  )
}
