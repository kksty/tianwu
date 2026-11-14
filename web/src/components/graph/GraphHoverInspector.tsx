// 悬停详情：只订阅 hover，不订阅图谱根状态。
//
// canvas 把 hover 发布到 view-store（docs/33 §4.5），这个小组件用 useHovered
// 订阅；指针移动时只有它重渲染，GraphPanel/Model 重建不跟着动。节点/边按 id
// 从 model.byId / model.edgesById 直接查，不走数组扫描。
import { GraphInspector } from '@/components/GraphInspector'
import type { GraphEdge, GraphModel } from '@/graph/model'
import { useHovered } from '@/graph/view-store'
import type { RunPulse } from '@/lib/types'

export function GraphHoverInspector({
  model,
  selected,
  pulseFor,
  width,
}: {
  model: GraphModel
  /** The pinned selection: hovering the selected item keeps its NodePanel
   *  visible instead of covering it with a hover card (same rule as before the
   *  view split). */
  selected?: string | null
  /** 运行中意图的 pulse；由 GraphPanel 从 board 里取，回调保持稳定。 */
  pulseFor?: (intentID: string) => RunPulse | undefined
  /** Shared width of the graph's right-hand overlays; the hover card is not
   *  draggable itself (it disappears when the pointer leaves it), it just matches
   *  the width the operator set on the pinned panel / 下发 dock. */
  width?: number
}) {
  const hovered = useHovered()
  if (!hovered) return null
  if (selected && hovered === selected) return null

  const node = model.byId.get(hovered) ?? null
  const edge: GraphEdge | null = node ? null : (model.edgesById?.get(hovered) ?? null)
  if (!node && !edge) return null

  const intent = node?.meta?.intent ?? edge?.intent
  const step = intent ? (model.stepByIntent?.get(intent.id) ?? null) : null
  const pulse = intent && pulseFor ? pulseFor(intent.id) : undefined

  return <GraphInspector node={node} edge={edge} pulse={pulse} model={node?.model ?? step?.model} width={width} />
}
