import { createContext, useContext, type ReactNode } from 'react'
import { cn } from '@cyber/theme'
import { Callout } from '@cyber/ui'
import { useScrollMemory } from '@/lib/view-memory'

/**
 * Project-level chrome injected into every page header while a project is open:
 * the live board counters and the controls that act on the whole engagement
 * (pause/resume, new direction, run a round). The project shell no longer draws
 * a second title bar of its own — one banner per page.
 *
 * Header hierarchy (top → bottom, strongest → weakest):
 *   1. the page title           20px / 600 / ink-1
 *   2. the actions              one primary CTA, the rest outline / ghost
 *   3. the subtitle             12.5px / ink-2, capped to a readable measure
 *   4. the eyebrow + meta chips 11px / ink-3, mono for counters
 * The eye lands on the title, then the primary action, and only then reads the
 * supporting counters — the inverse of a header where everything is a chip.
 */
export interface ProjectChrome {
  stats?: ReactNode
  controls?: ReactNode
  error?: string
  projectName?: string
}

export const ProjectChromeContext = createContext<ProjectChrome | null>(null)

interface PageProps {
  title: string
  subtitle?: ReactNode
  /** Status/meta chips: snapshot, live toggle, token totals. Rendered on the
   *  lower header row so the top row reads "what can I do". */
  meta?: ReactNode
  actions?: ReactNode
  children: ReactNode
  /** Views that manage their own scrolling (split panes) pass false. */
  scroll?: boolean
  className?: string
}

/** The frame every view sits in: title row, then a body that owns the scroll. */
export function Page({ title, subtitle, meta, actions, children, scroll = true, className }: PageProps) {
  const chrome = useContext(ProjectChromeContext)
  // The reading position survives leaving and re-entering a view (docs/34 §2).
  // A page that owns its own panes (`scroll={false}`) opts out by not attaching
  // the ref — the hook is still mounted, so hooks never become conditional.
  const scrollRef = useScrollMemory<HTMLDivElement>()
  const hasTopRow = Boolean(chrome?.controls || actions)
  const hasMetaRow = Boolean(chrome?.stats || meta)
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <header className="tw-page-header flex shrink-0 flex-col gap-3.5 border-b border-hairline px-5 py-4 sm:px-6 lg:px-8 xl:flex-row xl:items-start xl:gap-x-8">
        <div className="min-w-0 flex-1">
          <div className="tw-eyebrow flex min-w-0 items-center gap-1.5">
            <span className="truncate">{chrome ? (chrome.projectName || '项目工作区') : '全局控制台'}</span>
            <span aria-hidden="true" className="text-ink-3/45">/</span>
            <span className="truncate normal-case tracking-normal text-ink-2">{title}</span>
          </div>
          <h1 className="mt-1.5 break-words text-[20px] font-semibold leading-7 tracking-tight text-ink">{title}</h1>
          {subtitle && (
            <div className="tw-subtitle mt-1 max-w-[760px]">{subtitle}</div>
          )}
        </div>
        <div className="flex w-full min-w-0 flex-col items-start gap-2 xl:ml-auto xl:w-auto xl:shrink-0 xl:items-end">
          {hasTopRow && (
            <div className="flex w-full max-w-full flex-wrap items-center gap-2 xl:w-auto xl:justify-end">
              {chrome?.controls}
              {actions}
            </div>
          )}
          {hasMetaRow && (
            <div className="flex w-full max-w-full flex-wrap items-center gap-2 xl:w-auto xl:justify-end">
              {chrome?.stats}
              {meta}
            </div>
          )}
        </div>
      </header>
      {chrome?.error && (
        <div className="shrink-0 px-5 pt-4 sm:px-6 lg:px-8" role="alert">
          <Callout tone="destructive">{chrome.error}</Callout>
        </div>
      )}
      <div
        ref={scroll ? scrollRef : null}
        // Stable handle for the acceptance scripts that check scroll memory
        // (docs/34 §2): only the page body that actually owns the scroll.
        data-testid={scroll ? 'page-scroll' : undefined}
        className={cn(
          'min-h-0 min-w-0 flex-1',
          scroll ? 'overflow-y-auto px-5 py-5 sm:px-6 lg:px-8' : 'overflow-hidden',
          className,
        )}
      >
        {children}
      </div>
    </div>
  )
}

/**
 * A titled block inside a page. Cards stay flat: one hairline border and a
 * card tint carry the structure — no nesting of "card inside card inside card",
 * and no drop shadow stacked under a tinted header.
 *
 * Extra `<section>` attributes (`data-testid`, `aria-*`, `id`) pass through, so
 * a page that needs a stable test hook for its card can use the real card
 * component instead of hand-rolling a `tw-panel` section.
 */
export function Panel({
  title,
  extra,
  children,
  className,
  bodyClassName,
  ...rest
}: {
  title?: ReactNode
  extra?: ReactNode
  children: ReactNode
  className?: string
  bodyClassName?: string
} & Omit<React.HTMLAttributes<HTMLElement>, 'title' | 'children'>) {
  return (
    <section {...rest} className={cn('tw-panel min-w-0 rounded-xl', className)}>
      {(title || extra) && (
        <header className="flex flex-wrap items-center gap-2 border-b border-hairline px-4 py-3">
          <div className="tw-section-title min-w-0">{title}</div>
          <div className="ml-auto flex items-center gap-2">{extra}</div>
        </header>
      )}
      <div className={cn('p-4', bodyClassName)}>{children}</div>
    </section>
  )
}
