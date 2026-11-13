import { Skeleton, SkeletonList } from '@cyber/ui'

/**
 * Suspense fallback shaped like <Page>: same header strip and card body, so the
 * frame does not jump when the lazy chunk lands and only the content appears.
 * Shared by both shells (global pages and the project rail's panels) so the two
 * do not drift into two different loading looks.
 */
export function PageSkeleton() {
  return (
    <div className="flex h-full min-h-0 flex-col" aria-busy="true">
      <div className="tw-page-header flex shrink-0 items-center gap-3 border-b border-border px-4 py-5 sm:px-6 lg:px-7">
        <div className="min-w-0 space-y-2">
          <Skeleton className="h-2 w-20" />
          <Skeleton className="h-5 w-24" />
          <Skeleton className="h-3 w-36 sm:w-64" />
        </div>
        <Skeleton className="ml-auto h-7 w-20 shrink-0" />
      </div>
      <div className="min-h-0 flex-1 overflow-hidden px-5 py-3.5">
        <div className="grid grid-cols-1 gap-3 xl:grid-cols-3">
          <Skeleton className="h-20 rounded-lg" />
          <Skeleton className="h-20 rounded-lg" />
          <Skeleton className="h-20 rounded-lg" />
        </div>
        <div className="mt-3 rounded-lg border border-border bg-card p-3.5">
          <SkeletonList count={5} />
        </div>
      </div>
    </div>
  )
}
