import type { LucideIcon } from 'lucide-react'

/** One rail item: icon + label, with the active row reading as "you are here". */
export function SideNav({
  active,
  icon: Icon,
  label,
  onClick,
  onIntent,
  badge,
}: {
  active: boolean
  icon: LucideIcon
  label: string
  onClick: () => void
  /**
   * Fired when the row is reached for, not clicked. The shell uses it to start
   * downloading the page's chunk so the click swaps content instead of showing
   * the fallback. Must stay side-effect-only: it never changes the row.
   */
  onIntent?: () => void
  badge?: number
}) {
  return (
    <button
      type="button"
      aria-current={active ? 'page' : undefined}
      onClick={onClick}
      onMouseEnter={onIntent}
      onFocus={onIntent}
      className={
        'group relative flex min-h-10 w-full items-center gap-3 rounded-lg border px-3 py-2 text-left text-[13px] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 ' +
        (active
          ? 'border-primary/15 bg-primary/[0.08] font-medium text-primary shadow-[inset_3px_0_0_var(--primary)]'
          : 'border-transparent text-muted-foreground hover:bg-muted/80 hover:text-foreground')
      }
    >
      <Icon className="h-4 w-4 shrink-0" />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {badge !== undefined && <span className="rounded-full bg-amber-500/15 px-1.5 py-0.5 font-mono text-[10px] text-amber-500">{badge}</span>}
    </button>
  )
}
