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
  // One icon size (h-4) and one button size (icon-sm, 32px) across the row; the
  // pressed state comes from `active`, so a switch that is on cannot be mistaken
  // for a hover. Hairlines split "capture" | "camera" | "layout/view".
  const divider = <span className="mx-0.5 h-5 w-px shrink-0 bg-hairline" aria-hidden />
  return (
    <div
      className="absolute right-3 top-3 flex w-[128px] flex-row flex-wrap justify-center gap-1 rounded-xl border border-hairline bg-card/90 p-1.5 shadow-lifted backdrop-blur sm:w-auto sm:flex-nowrap sm:justify-end"
      role="toolbar"
      aria-label="图谱视图控件"
    >
      <Button
        size="icon-sm"
        variant="ghost"
        active={chatDock}
        title="下发：给指挥官或全体执行者发一句话（右侧面板）"
        aria-label="下发"
        onClick={onToggleChatDock}
      >
        <MessageSquare className="h-4 w-4" />
      </Button>
      {divider}
      <Button size="icon-sm" variant="ghost" title="放大" aria-label="放大" onClick={() => onZoom(1.25)}>
        <ZoomIn className="h-4 w-4" />
      </Button>
      <Button size="icon-sm" variant="ghost" title="缩小" aria-label="缩小" onClick={() => onZoom(0.8)}>
        <ZoomOut className="h-4 w-4" />
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        title="适配画布（键盘：方向键移动选择 · Enter 打开 · Esc 取消）"
        aria-label="适配画布"
        onClick={onFit}
      >
        <Maximize2 className="h-4 w-4" />
      </Button>
      <Button size="icon-sm" variant="ghost" title="重新布局" aria-label="重新布局" onClick={onReheat}>
        <RotateCcw className="h-4 w-4" />
      </Button>
      {divider}
      <Button
        size="icon-sm"
        variant="ghost"
        active={layout === 'layered'}
        title="链路树：起点 → 每一步意图 → 它产出的事实；目标达成后接到证据事实上（未达成的终点在左下终点卡）"
        aria-label="链路树布局"
        onClick={() => onLayout('layered')}
      >
        <GitBranch className="h-4 w-4" />
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        active={layout === 'force'}
        title="力导向：按连接关系铺开（旧视图）"
        aria-label="力导向布局"
        onClick={() => onLayout('force')}
      >
        <Network className="h-4 w-4" />
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        active={showLabels}
        title={showLabels ? '隐藏意图标签' : '显示意图标签'}
        aria-label="意图标签"
        onClick={onToggleLabels}
      >
        <Tags className="h-4 w-4" />
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        active={listView}
        title="列表视图（画布的可访问等价入口；键盘可达，Esc 关闭）"
        aria-label="列表视图"
        onClick={onToggleListView}
      >
        <List className="h-4 w-4" />
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        active={focusOverlay}
        disabled={!focusOverlayAvailable}
        title={
          focusOverlayAvailable
            ? '焦点叠层：按本次派发的焦点投影给链路分热/温/冷（hot 加环，cold 淡化）；只影响绘制'
            : '焦点叠层需要先选中一个意图'
        }
        aria-label="焦点叠层"
        onClick={onToggleFocusOverlay}
      >
        <Focus className="h-4 w-4" />
      </Button>
    </div>
  )
}
