import { Skeleton, SkeletonList } from '@cyber/ui'

/**
 * Suspense fallback shaped like <Page>: same header strip and card body, so the
 * frame does not jump when the lazy chunk lands and only the content appears.
 * Shared by both shells (global pages and the project rail's panels) so the two
 * do not drift into two different loading looks.
 */
export function PageSkeleton() {
  return (
    <div className="tw-rise flex h-full min-h-0 flex-col" aria-busy="true" aria-label="页面加载中">
      <div className="tw-page-header flex shrink-0 items-start gap-3 border-b border-hairline px-5 py-4 sm:px-6 lg:px-8">
        <div className="min-w-0 space-y-2">
          <Skeleton className="h-2.5 w-24" />
          <Skeleton className="h-5 w-28" />
          <Skeleton className="h-3 w-40 sm:w-72" />
        </div>
        <Skeleton className="ml-auto h-8 w-24 shrink-0" />
      </div>
      <div className="min-h-0 flex-1 overflow-hidden px-5 py-5 sm:px-6 lg:px-8">
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
          <Skeleton className="h-20 rounded-xl" />
          <Skeleton className="h-20 rounded-xl" />
          <Skeleton className="h-20 rounded-xl" />
        </div>
        <div className="mt-4 rounded-xl border border-hairline bg-card p-4">
          <SkeletonList count={5} />
        </div>
      </div>
    </div>
  )
}
