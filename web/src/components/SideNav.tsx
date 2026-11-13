import type { LucideIcon } from 'lucide-react'
import { cn } from '@cyber/theme'

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
      className={cn(
        'group relative flex min-h-9 w-full items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-left text-[13px] transition-colors',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50',
        active
          // The accent is a 2px marker + icon, never a full red row: the rail
          // stays background-toned so the page content keeps the visual weight.
          ? 'bg-primary/[0.09] font-medium text-ink shadow-[inset_2px_0_0_var(--primary)]'
          : 'font-normal text-ink-2 hover:bg-surface-2 hover:text-ink',
      )}
    >
      <Icon className={cn('h-4 w-4 shrink-0', active ? 'text-primary' : 'text-ink-3 group-hover:text-ink-2')} />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {badge !== undefined && badge > 0 && (
        <span className="tw-nums rounded-full bg-warning/15 px-1.5 py-0.5 font-mono text-[11px] leading-none text-warning">
          {badge}
        </span>
      )}
    </button>
  )
}
