// The graph canvas toolbar (T6, docs/33 §4.6): the historical view controls
// plus the two new switches (list view, focus overlay). Buttons only report
// intent; the page owns the state and the canvas ref.
//
// The row wraps on narrow screens (390px usable): at that width the filter card
// occupies the left column, so the toolbar keeps a wrapped 128px column on the
// right instead of running underneath the card.

import {
  Focus,
  GitBranch,
  List,
  Maximize2,
  MessageSquare,
  Network,
  RotateCcw,
  Tags,
  ZoomIn,
  ZoomOut,
} from 'lucide-react'
import { Button } from '@cyber/ui'
import type { LayoutMode } from '@/graph/sim'

export interface GraphToolbarProps {
  chatDock: boolean
  onToggleChatDock: () => void
  layout: LayoutMode
  onLayout: (layout: LayoutMode) => void
  showLabels: boolean
  onToggleLabels: () => void
  /** The accessible list equivalent of the canvas. */
  listView: boolean
  onToggleListView: () => void
  /** Focus overlay (hot/warm/cold) for the selected direction's chain. */
  focusOverlay: boolean
  /** False while no intent is selected: an overlay without a focus is noise. */
  focusOverlayAvailable: boolean
  onToggleFocusOverlay: () => void
  onZoom: (factor: number) => void
  onFit: () => void
  onReheat: () => void
}

export function GraphToolbar({
  chatDock,
  onToggleChatDock,
  layout,
  onLayout,
  showLabels,
  onToggleLabels,
  listView,
  onToggleListView,
  focusOverlay,
  focusOverlayAvailable,
  onToggleFocusOverlay,
  onZoom,
  onFit,
  onReheat,
}: GraphToolbarProps) {
  return (
    <div
      className="absolute right-3 top-3 flex w-[128px] flex-row flex-wrap justify-center gap-1 rounded-lg border border-border bg-card/85 p-1 shadow-lifted backdrop-blur sm:w-auto sm:flex-nowrap sm:justify-end"
      role="toolbar"
      aria-label="图谱视图控件"
    >
      <Button
        size="icon-xs"
        variant={chatDock ? 'soft' : 'ghost'}
        title="下发：给指挥官或全体执行者发一句话（右侧面板）"
        aria-label="下发"
        onClick={onToggleChatDock}
      >
        <MessageSquare className="h-3.5 w-3.5" />
      </Button>
      <Button size="icon-xs" variant="ghost" title="放大" aria-label="放大" onClick={() => onZoom(1.25)}>
        <ZoomIn className="h-3.5 w-3.5" />
      </Button>
      <Button size="icon-xs" variant="ghost" title="缩小" aria-label="缩小" onClick={() => onZoom(0.8)}>
        <ZoomOut className="h-3.5 w-3.5" />
      </Button>
      <Button
        size="icon-xs"
        variant="ghost"
        title="适配画布（键盘：方向键移动选择 · Enter 打开 · Esc 取消）"
        aria-label="适配画布"
        onClick={onFit}
      >
        <Maximize2 className="h-3.5 w-3.5" />
      </Button>
      <Button size="icon-xs" variant="ghost" title="重新布局" aria-label="重新布局" onClick={onReheat}>
        <RotateCcw className="h-3.5 w-3.5" />
      </Button>
      <Button
        size="icon-xs"
        variant={layout === 'layered' ? 'soft' : 'ghost'}
        title="链路树：起点 → 每一步意图 → 它产出的事实；目标达成后接到证据事实上（未达成的终点在左下终点卡）"
        aria-label="链路树布局"
        onClick={() => onLayout('layered')}
      >
        <GitBranch className="h-3.5 w-3.5" />
      </Button>
      <Button
        size="icon-xs"
        variant={layout === 'force' ? 'soft' : 'ghost'}
        title="力导向：按连接关系铺开（旧视图）"
        aria-label="力导向布局"
        onClick={() => onLayout('force')}
      >
        <Network className="h-3.5 w-3.5" />
      </Button>
      <Button
        size="icon-xs"
        variant={showLabels ? 'soft' : 'ghost'}
        title={showLabels ? '隐藏意图标签' : '显示意图标签'}
        aria-label="意图标签"
        onClick={onToggleLabels}
      >
        <Tags className="h-3.5 w-3.5" />
      </Button>
      <Button
        size="icon-xs"
        variant={listView ? 'soft' : 'ghost'}
        title="列表视图（画布的可访问等价入口；键盘可达，Esc 关闭）"
        aria-label="列表视图"
        onClick={onToggleListView}
      >
        <List className="h-3.5 w-3.5" />
      </Button>
      <Button
        size="icon-xs"
        variant={focusOverlay ? 'soft' : 'ghost'}
        disabled={!focusOverlayAvailable}
        title={
          focusOverlayAvailable
            ? '焦点叠层：按本次派发的焦点投影给链路分热/温/冷（hot 加环，cold 淡化）；只影响绘制'
            : '焦点叠层需要先选中一个意图'
        }
        aria-label="焦点叠层"
        onClick={onToggleFocusOverlay}
      >
        <Focus className="h-3.5 w-3.5" />
      </Button>
    </div>
  )
}
