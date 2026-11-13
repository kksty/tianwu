import { lazy, Suspense, useEffect } from 'react'
import { Skeleton, SkeletonList } from '@cyber/ui'
import { api, gotoProject, projectID } from '@/lib/api'
import { useLoad, usePoll } from '@/lib/hooks'
import { pageTitle } from '@/lib/page-title'

// The two shells are split: opening the instance page must not download the
// graph view (and its xterm terminal), and vice versa.
const GlobalShell = lazy(() => import('@/views/GlobalShell').then((m) => ({ default: m.GlobalShell })))
const GraphView = lazy(() => import('@/views/GraphView').then((m) => ({ default: m.GraphView })))

// The tab starts as the <title> written in index.html. Whoever names the tab
// first owns it; the project shell only claims an unnamed one, so the title a
// view refines ("项目 · 当前面板") is not demoted when the project list arrives.
const BOOT_TITLE = document.title

/**
 * First paint while a shell chunk downloads: the frame the console is about to
 * have (208px rail, top bar, content), so nothing jumps when it swaps in.
 */
function Boot() {
  return (
    <div className="flex h-full w-full flex-col overflow-hidden lg:flex-row" aria-busy="true">
      <div className="flex h-14 shrink-0 items-center border-b border-border px-4 lg:hidden"><Skeleton className="h-6 w-24" /></div>
      <aside className="tw-rail hidden w-[232px] shrink-0 flex-col border-r border-border lg:flex">
        <div className="flex h-14 shrink-0 items-center border-b border-border px-3.5">
          <Skeleton className="h-6 w-28" />
        </div>
        <div className="min-h-0 flex-1 overflow-hidden p-1.5">
          <SkeletonList count={6} />
        </div>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex h-14 shrink-0 items-center gap-3 border-b border-border px-5">
          <Skeleton className="h-5 w-32" />
          <Skeleton className="ml-auto h-7 w-24 shrink-0" />
        </div>
        <div className="min-h-0 flex-1 overflow-hidden px-5 py-3.5">
          <div className="space-y-3">
            <Skeleton className="h-24 rounded-lg" />
            <SkeletonList count={5} />
          </div>
        </div>
      </div>
    </div>
  )
}

export default function App() {
  // The URL is the route: /p/<id>/ is one engagement, /g/ is the instance.
  const pid = projectID()
  return (
    <Suspense fallback={<Boot />}>{pid ? <ProjectApp pid={pid} /> : <GlobalShell />}</Suspense>
  )
}

function ProjectApp({ pid }: { pid: string }) {
  const projects = useLoad(api.projects, [])
  const board = usePoll(api.board, 5000)

  // A stale/unknown project id falls back to the first project (or the instance
  // view when there is none).
  useEffect(() => {
    if (!projects.data) return
    if (projects.data.some((p) => p.id === pid)) return
    const first = projects.data[0]
    if (first) gotoProject(first.id)
    else window.location.assign('/g/')
  }, [pid, projects.data])

  // Name the engagement in the tab once the project list is in. GraphView adds
  // the open panel on top of this; the base title only fills an unnamed tab
  // (see BOOT_TITLE), so a refresh cannot overwrite that refined title.
  useEffect(() => {
    if (!projects.data || document.title !== BOOT_TITLE) return
    const current = projects.data.find((p) => p.id === pid)
    if (current) document.title = pageTitle([current.name])
  }, [pid, projects.data])

  return (
    <GraphView
      current={pid}
      projects={projects.data ?? []}
      board={board.data}
      loadError={projects.error || board.error}
      onRefresh={() => {
        projects.reload()
        board.reload()
      }}
    />
  )
}
