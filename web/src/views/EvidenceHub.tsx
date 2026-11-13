// 证据/发现 group (docs/34 §1.2): 证据 / HTTP 流量 / 资产 share one page. A
// container, not a rewrite: each child keeps its own <Page> header and logic.

import { TabBar, type TabItem } from '@/components/TabBar'
import { Assets } from './Assets'
import { EvidenceView } from './Evidence'
import { Traffic } from './Traffic'

export type EvidenceTab = 'evidence' | 'traffic' | 'assets'

const EVIDENCE_TABS: readonly TabItem<EvidenceTab>[] = [
  { id: 'evidence', label: '证据' },
  { id: 'traffic', label: 'HTTP 流量' },
  { id: 'assets', label: '资产' },
]

export function EvidenceHub({
  tab,
  onTab,
}: {
  tab: EvidenceTab
  onTab: (t: EvidenceTab) => void
}) {
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col bg-background">
      <TabBar tabs={EVIDENCE_TABS} tab={tab} onTab={onTab} ariaLabel="证据" />
      <div className="min-h-0 min-w-0 flex-1 overflow-hidden">
        {tab === 'evidence' ? <EvidenceView /> : tab === 'traffic' ? <Traffic /> : <Assets />}
      </div>
    </div>
  )
}
