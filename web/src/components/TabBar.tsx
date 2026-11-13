// The compact tab strip that sits above a merged page group (docs/34 §1.1).
//
// The strip is presentational: the active tab comes from the shell's `?tab=`
// URL (aliases resolve in shell-router), so a reload, a backlink or Back/Forward
// restores the tab without component state. Each tab carries a stable test id
// (`tab-<tab>`) so the acceptance scripts can address it without text matching.
//
// Keyboard: Left/Right move the roving focus and activate the neighbour (the
// standard tablist behaviour); Home/End jump to the ends. Enter/Space are the
// native button activation.

import { useRef, type KeyboardEvent as ReactKeyboardEvent } from 'react'

export interface TabItem<T extends string = string> {
  id: T
  label: string
}

export function TabBar<T extends string>({
  tabs,
  tab,
  onTab,
  ariaLabel = '视图标签',
}: {
  tabs: readonly TabItem<T>[]
  tab: T
  onTab: (tab: T) => void
  ariaLabel?: string
}) {
  const listRef = useRef<HTMLDivElement | null>(null)

  function select(next: number, focus: boolean) {
    if (tabs.length === 0) return
    const index = ((next % tabs.length) + tabs.length) % tabs.length
    if (focus) {
      const buttons = listRef.current?.querySelectorAll<HTMLButtonElement>('[role="tab"]')
      buttons?.[index]?.focus()
    }
    onTab(tabs[index].id)
  }

  function onKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    const current = tabs.findIndex((item) => item.id === tab)
    switch (event.key) {
      case 'ArrowRight':
        event.preventDefault()
        select(current + 1, true)
        break
      case 'ArrowLeft':
        event.preventDefault()
        select(current - 1, true)
        break
      case 'Home':
        event.preventDefault()
        select(0, true)
        break
      case 'End':
        event.preventDefault()
        select(tabs.length - 1, true)
        break
      default:
        break
    }
  }

  return (
    <div
      ref={listRef}
      role="tablist"
      aria-label={ariaLabel}
      data-testid="view-tabs"
      onKeyDown={onKeyDown}
      className="flex shrink-0 items-center gap-1 overflow-x-auto border-b border-border/80 px-3 py-1.5 sm:px-4"
    >
      {tabs.map((item) => {
        const active = item.id === tab
        return (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={active}
            tabIndex={active ? 0 : -1}
            data-testid={`tab-${item.id}`}
            onClick={() => onTab(item.id)}
            className={
              active
                ? 'shrink-0 rounded-md bg-muted px-2.5 py-1 text-[12px] font-medium text-foreground transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring'
                : 'shrink-0 rounded-md px-2.5 py-1 text-[12px] text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring'
            }
          >
            {item.label}
          </button>
        )
      })}
    </div>
  )
}
