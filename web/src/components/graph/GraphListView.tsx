// 列表视图：画布的键盘/读屏等价入口。
//
// 图上的每一步都能在这里用文字读到（类型 / 标签 / 状态），顺序与画布键盘导航
// 同源（graphNodeOrder：depth → y → x → id），所以“下一行”就是“在图上往下一层/
// 往右一个”。容器自己是一个 listbox tab stop：打开列表时它获得焦点，Esc 关闭后
// 把焦点还给主画布（data-testid="graph-canvas"）。不新增依赖。
import { useEffect, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent } from 'react'
import { Button, EmptyState } from '@cyber/ui'
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
  // Ghost rings are not openable: the step row is the direction, and the canvas
  // deliberately picks nothing on a ghost (see graph/hit.ts). Listing them would
  // add a second row for the same direction that pins a panel the map refuses to
  // open — so the list, like the canvas keyboard order, leaves them out.
  const ordered = useMemo(
    () => graphNodeOrder(model.nodes.filter((node) => node.type !== 'ghost')),
    [model],
  )
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
      <div className="flex items-center gap-2 border-b border-hairline bg-card/95 px-3 py-2.5">
        <span className="tw-section-title shrink-0">节点列表</span>
        <span className="min-w-0 truncate text-[12px] text-ink-3">
          <span className="tw-nums font-mono font-semibold text-ink-2">{ordered.length}</span> 个节点 · 画布的键盘等价入口
        </span>
        <span className="ml-auto hidden shrink-0 text-[11px] text-ink-3 md:inline">
          ↑↓ 选择 · Enter 打开 · Esc 返回画布
        </span>
        <Button
          size="xs"
          variant="ghost"
          className="shrink-0"
          onClick={() => {
            onClose()
            returnFocusToCanvas()
          }}
          title="返回画布（Esc）"
        >
          关闭
        </Button>
      </div>
      <div
        ref={containerRef}
        role="listbox"
        aria-label="图谱节点列表"
        aria-activedescendant={ordered.length > 0 ? rowID(active) : undefined}
        tabIndex={0}
        onKeyDown={onKeyDown}
        className="min-h-0 flex-1 overflow-y-auto px-2 py-1.5 outline-none focus-visible:ring-1 focus-visible:ring-ring"
      >
        {ordered.length === 0 ? (
          <EmptyState
            compact
            className="py-10"
            title="这个快照里还没有可列出的节点。"
            description="图里出现意图或事实后，会在这里按与画布键盘导航相同的顺序列出。"
          />
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
                className={`flex h-9 cursor-pointer items-center gap-2 rounded-md px-2.5 text-[12px] transition-colors [contain-intrinsic-size:2.25rem] [content-visibility:auto] ${
                  isSelected
                    ? 'bg-primary/[0.09] text-ink shadow-[inset_2px_0_0_var(--primary)]'
                    : isActive
                      ? 'bg-surface-2 text-ink'
                      : 'text-ink-2 hover:bg-surface-2/70 hover:text-ink'
                }`}
              >
                <span className="w-[112px] shrink-0 truncate font-mono text-[11px] text-ink-3">
                  {typeText(node)}
                </span>
                <span className="min-w-0 flex-1 truncate">
                  {node.label || '（无标题）'}
                  {isMatch && (
                    <span className="ml-1.5 rounded bg-info/12 px-1.5 py-0.5 text-[11px] text-info">命中</span>
                  )}
                </span>
                <span className="w-[76px] shrink-0 truncate text-right text-[11px] text-ink-3">
                  {stateText(node)}
                </span>
                <span
                  className="hidden w-[136px] shrink-0 truncate font-mono text-[11px] text-ink-3/80 sm:block"
                  title={node.id}
                >
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
