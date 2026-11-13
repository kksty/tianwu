import { createContext, useContext, type ReactNode } from 'react'
import { cn } from '@cyber/theme'
import { Callout } from '@cyber/ui'
import { useScrollMemory } from '@/lib/view-memory'

/**
 * Project-level chrome injected into every page header while a project is open:
 * the live board counters and the controls that act on the whole engagement
 * (pause/resume, new direction, run a round). The project shell no longer draws
 * a second title bar of its own — one banner per page.
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
  actions?: ReactNode
  children: ReactNode
  /** Views that manage their own scrolling (split panes) pass false. */
  scroll?: boolean
  className?: string
}

/** The frame every view sits in: title row, then a body that owns the scroll. */
export function Page({ title, subtitle, actions, children, scroll = true, className }: PageProps) {
  const chrome = useContext(ProjectChromeContext)
  // The reading position survives leaving and re-entering a view (docs/34 §2).
  // A page that owns its own panes (`scroll={false}`) opts out by not attaching
  // the ref — the hook is still mounted, so hooks never become conditional.
  const scrollRef = useScrollMemory<HTMLDivElement>()
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <div className="tw-page-header flex shrink-0 flex-wrap items-center gap-x-6 gap-y-3 border-b border-border/80 px-4 py-4 sm:px-6 lg:px-7">
        <div className="min-w-0">
          <div className="mb-1 break-words text-[10px] font-medium tracking-[0.12em] text-muted-foreground [overflow-wrap:anywhere]">{chrome ? (chrome.projectName || '项目工作区') : '全局控制台'}<span className="mx-2 opacity-40">/</span>{title}</div>
          <h1 className="text-xl font-semibold tracking-tight text-foreground">{title}</h1>
          {subtitle && (
            <div className="mt-1 break-words text-[12px] leading-relaxed text-muted-foreground">{subtitle}</div>
          )}
        </div>
        <div className="ml-auto flex max-w-full flex-wrap items-center justify-end gap-2">
          {chrome?.stats}
          {chrome?.controls}
          {actions}
        </div>
      </div>
      {chrome?.error && <div className="shrink-0 px-4 pt-3 sm:px-6" role="alert"><Callout tone="destructive">{chrome.error}</Callout></div>}
      <div
        ref={scroll ? scrollRef : null}
        // Stable handle for the acceptance scripts that check scroll memory
        // (docs/34 §2): only the page body that actually owns the scroll.
        data-testid={scroll ? 'page-scroll' : undefined}
        className={cn(
          'min-h-0 min-w-0 flex-1',
          scroll ? 'overflow-y-auto px-4 py-5 sm:px-6 lg:px-7' : 'overflow-hidden',
          className,
        )}
      >
        {children}
      </div>
    </div>
  )
}

/** A titled block inside a page. Cards stay flat: the border is the structure. */
export function Panel({
  title,
  extra,
  children,
  className,
  bodyClassName,
}: {
  title?: ReactNode
  extra?: ReactNode
  children: ReactNode
  className?: string
  bodyClassName?: string
}) {
  return (
    <section className={cn('tw-panel min-w-0 rounded-xl border border-border/80 bg-card', className)}>
      {(title || extra) && (
        <header className="flex flex-wrap items-center gap-2 border-b border-border/65 px-4 py-3">
          <div className="text-xs font-medium tracking-wide text-foreground/80">
            {title}
          </div>
          <div className="ml-auto flex items-center gap-2">{extra}</div>
        </header>
      )}
      <div className={cn('p-4', bodyClassName)}>{children}</div>
    </section>
  )
}
