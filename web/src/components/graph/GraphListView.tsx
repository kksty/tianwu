// 列表视图：画布的键盘/读屏等价入口。
//
// 图上的每一步都能在这里用文字读到（类型 / 标签 / 状态），顺序与画布键盘导航
// 同源（graphNodeOrder：depth → y → x → id），所以“下一行”就是“在图上往下一层/
// 往右一个”。容器自己是一个 listbox tab stop：打开列表时它获得焦点，Esc 关闭后
// 把焦点还给主画布（data-testid="graph-canvas"）。不新增依赖。
import { useEffect, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent } from 'react'
import type { GraphModel, GraphNode } from '@/graph/model'
import { graphNodeOrder } from '@/lib/graph-filter'
import { statusLabel } from '@/lib/format'

/** 主画布的 testid（T5 的 GraphCanvas）；Esc 关闭后把焦点交回去。 */
const CANVAS_TESTID = 'graph-canvas'

function typeText(node: GraphNode): string {
  switch (node.type) {
    case 'project':
      return '项目'
    case 'fact':
      return `事实 · ${node.kind || '未分类'}`
    case 'step':
      return '意图'
    case 'ghost':
      return '意图 · 未产出'
    case 'goal':
      return '目标'
    case 'boundary':
      return '边界引用'
    default:
      return node.type
  }
}

function stateText(node: GraphNode): string {
  if (node.type === 'project') return '—'
  if (node.type === 'goal') return node.status === 'met' ? '已达成' : '未达成'
  return statusLabel(node.status)
}

function rowID(index: number): string {
  return `graph-list-row-${index}`
}

/** Esc 后主画布可能刚被父组件重新挂载：等一帧再聚焦，找不到就静默跳过。 */
function returnFocusToCanvas(): void {
  if (typeof window === 'undefined') return
  window.requestAnimationFrame(() => {
    const canvas = document.querySelector<HTMLElement>(`[data-testid="${CANVAS_TESTID}"]`)
    canvas?.focus({ preventScroll: true })
  })
}

export function GraphListView({
  model,
  selected,
  matches,
  onSelect,
  onClose,
}: {
  model: GraphModel
  selected: string | null
  /** Optional search hits; a matched row gets the same info tint as on canvas. */
  matches?: Set<string> | null
  onSelect: (id: string | null) => void
  onClose: () => void
}) {
  const ordered = useMemo(() => graphNodeOrder(model.nodes), [model])
  const containerRef = useRef<HTMLDivElement | null>(null)
  const selectedIndex = selected ? ordered.findIndex((node) => node.id === selected) : -1
  const [active, setActive] = useState(() => (selectedIndex >= 0 ? selectedIndex : 0))

  // 选中项变化时（画布点选/深链）列表跟随，但保留可用的活动行。
  useEffect(() => {
    if (selectedIndex >= 0) setActive(selectedIndex)
  }, [selectedIndex])

  // A shrinking model must not leave the active row past the end.
  useEffect(() => {
    setActive((current) => (ordered.length === 0 ? 0 : Math.min(current, ordered.length - 1)))
  }, [ordered.length])

  // 打开列表就聚焦容器：键盘用户不必先 Tab 进一个 5000 行的滚动区。
  useEffect(() => {
    containerRef.current?.focus()
  }, [])

  // 活动行保持在滚动可视区内；用 index 派生 id，不额外持有 5000 个 ref。
  useEffect(() => {
    if (typeof document === 'undefined' || ordered.length === 0) return
    document.getElementById(rowID(active))?.scrollIntoView({ block: 'nearest' })
  }, [active, ordered.length])

  const move = (delta: number) => {
    if (ordered.length === 0) return
    setActive((current) => Math.max(0, Math.min(ordered.length - 1, current + delta)))
  }

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault()
        move(1)
        break
      case 'ArrowUp':
        event.preventDefault()
        move(-1)
        break
      case 'PageDown':
        event.preventDefault()
        move(10)
        break
      case 'PageUp':
        event.preventDefault()
        move(-10)
        break
      case 'Home':
        event.preventDefault()
        setActive(0)
        break
      case 'End':
        event.preventDefault()
        setActive(Math.max(ordered.length - 1, 0))
        break
      case 'Enter': {
        event.preventDefault()
        const node = ordered[active]
        if (node) onSelect(node.id)
        break
      }
      case 'Escape':
        event.preventDefault()
        onClose()
        returnFocusToCanvas()
        break
      default:
        break
    }
  }

  return (
    <div
      className="pointer-events-auto absolute inset-0 z-10 flex flex-col bg-background/95 backdrop-blur"
      data-testid="graph-list-view"
    >
      <div className="flex items-center gap-2 border-b border-border px-3 py-2 text-[12px]">
        <span className="font-semibold text-foreground">节点列表</span>
        <span className="text-muted-foreground">{ordered.length} 个节点 · 画布的键盘等价入口</span>
        <span className="ml-auto text-[10px] text-muted-foreground">↑↓ 选择 · Enter 打开 · Esc 返回画布</span>
        <button
          type="button"
          onClick={() => {
            onClose()
            returnFocusToCanvas()
          }}
          className="rounded px-1.5 py-0.5 text-muted-foreground hover:bg-muted/60 hover:text-foreground"
          title="返回画布（Esc）"
        >
          关闭
        </button>
      </div>
      <div
        ref={containerRef}
        role="listbox"
        aria-label="图谱节点列表"
        aria-activedescendant={ordered.length > 0 ? rowID(active) : undefined}
        tabIndex={0}
        onKeyDown={onKeyDown}
        className="min-h-0 flex-1 overflow-y-auto px-2 py-1 outline-none focus-visible:ring-1 focus-visible:ring-ring"
      >
        {ordered.length === 0 ? (
          <div className="px-2 py-6 text-center text-[12px] text-muted-foreground">这个快照里还没有可列出的节点。</div>
        ) : (
          ordered.map((node, index) => {
            const isActive = index === active
            const isSelected = node.id === selected
            const isMatch = Boolean(matches?.has(node.id))
            return (
              <div
                key={node.id}
                id={rowID(index)}
                role="option"
                aria-selected={isSelected}
                aria-current={isActive ? 'true' : undefined}
                onClick={() => {
                  setActive(index)
                  onSelect(node.id)
                }}
                onMouseEnter={() => setActive(index)}
                className={`flex cursor-pointer items-baseline gap-2 rounded px-2 py-1 text-[12px] [contain-intrinsic-size:1.75rem] [content-visibility:auto] ${
                  isSelected
                    ? 'bg-primary/15 text-foreground'
                    : isActive
                      ? 'bg-muted/60 text-foreground'
                      : 'text-muted-foreground hover:bg-muted/40'
                }`}
              >
                <span className="w-[104px] shrink-0 font-mono text-[10.5px] text-muted-foreground">{typeText(node)}</span>
                <span className="min-w-0 flex-1 truncate">
                  {node.label || '（无标题）'}
                  {isMatch && (
                    <span className="ml-1 rounded bg-info/15 px-1 text-[9px] text-info">命中</span>
                  )}
                </span>
                <span className="w-[72px] shrink-0 text-right text-[10.5px] text-muted-foreground">{stateText(node)}</span>
                <span className="w-[132px] shrink-0 truncate font-mono text-[10px] opacity-60" title={node.id}>
                  {node.id}
                </span>
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}
