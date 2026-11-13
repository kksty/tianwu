import { useEffect, useState, type ReactNode } from 'react'
import { Menu } from 'lucide-react'
import { Button, Sheet, SheetContent, SheetDescription, SheetTitle, SheetTrigger } from '@cyber/ui'
import { Brand } from './Brand'

/** Both workspaces share the same rail; on small screens it becomes a modal
 * drawer so the operator can still use the page at its full width. */
export function ConsoleRail({ title, active, context, children, footer }: {
  title: string
  active: string
  context?: ReactNode
  children: ReactNode
  footer: ReactNode
}) {
  const [open, setOpen] = useState(false)
  useEffect(() => { setOpen(false) }, [active])
  useEffect(() => {
    const wide = window.matchMedia('(min-width: 1024px)')
    const resize = () => { if (wide.matches) setOpen(false) }
    wide.addEventListener('change', resize)
    return () => wide.removeEventListener('change', resize)
  }, [])
  const contents = <>
    <div className="shrink-0 px-5 pb-4 pt-6">
      <Brand />
      <div className="mt-4 flex items-center gap-2 text-[11px] tracking-wider text-muted-foreground"><span className="h-1 w-1 rounded-full bg-primary/80" />{title}</div>
    </div>
    {context && <div className="mx-3 mb-3 shrink-0">{context}</div>}
    {children}
    <div className="m-3 mt-auto shrink-0 border-t border-border/70 pt-3">{footer}</div>
  </>
  return <>
    <div className="flex h-14 shrink-0 items-center justify-between border-b border-border bg-card px-4 lg:hidden">
      <Brand />
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetTrigger asChild><Button size="sm" variant="outline" aria-label="打开导航"><Menu className="h-4 w-4" />导航</Button></SheetTrigger>
        <SheetContent side="left" closeLabel="关闭导航" className="flex w-[280px] max-w-[85vw] flex-col gap-0 p-0" onClick={event => {
          if (event.target instanceof Element && event.target.closest('nav button')) setOpen(false)
        }}>
          <SheetTitle className="sr-only">{title}导航</SheetTitle>
          <SheetDescription className="sr-only">选择页面；按 Escape 或关闭按钮返回工作区。</SheetDescription>
          {contents}
        </SheetContent>
      </Sheet>
    </div>
    <aside className="tw-rail hidden w-[232px] shrink-0 flex-col border-r border-border/80 lg:flex">{contents}</aside>
  </>
}
