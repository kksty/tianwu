import type { ReactNode } from 'react'
import { cn } from '@cyber/theme'

/**
 * 左上角品牌锁排: pure 「天吴」艺术字, no icon.
 *
 * The wordmark is deliberately typographic: 楷体/衬线字形 + 暖米→珊瑚渐变 +
 * 一道极淡的篆刻投影, with a small vermillion seal dot as the only graphic
 * accent. It is a lockup, not decoration: the name must read at a glance in a
 * 208px rail, and the TIANWU caption gives the latin fallback.
 *
 * The favicon keeps the seal mark (a square tab icon cannot be a wordmark at
 * 16px), so the identity still has one graphic element outside the shell.
 */
export function Brand({ badge, className }: { badge?: ReactNode; className?: string }) {
  return (
    <div className={cn('flex min-w-0 items-center', className)}>
      <div className="flex min-w-0 flex-col justify-center gap-[5px]">
        <span className="flex items-start gap-[2px]">
          <span className="tw-brand-cn">天吴</span>
          <span aria-hidden="true" className="tw-brand-dot" />
        </span>
        <span className="tw-brand-latin">TIANWU</span>
      </div>
      {badge ? <div className="ml-auto shrink-0">{badge}</div> : null}
    </div>
  )
}
