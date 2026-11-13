import { Suspense, lazy, useEffect, type ComponentType } from 'react'
import {
  BookOpen,
  FolderKanban,
  Radar,
  Settings as SettingsIcon,
  ShieldAlert,
  SquareTerminal,
  Waypoints,
} from 'lucide-react'
import { Button, Callout } from '@cyber/ui'
import { ConsoleRail } from '@/components/ConsoleRail'
import { PageSkeleton } from '@/components/PageSkeleton'
import { SideNav } from '@/components/SideNav'
import { api, gotoProject } from '@/lib/api'
import { useConsoleLocation, useLoad } from '@/lib/hooks'
import { navigate } from '@/lib/navigation'
import { pageTitle } from '@/lib/page-title'
import { createPrefetchRegistry } from '@/lib/prefetch'

// The instance view: the fleet 态势 page, projects, the shared vulnerability
// library, the template library and the model settings. None of it belongs to
// one engagement, so it lives outside /p/<id>/ and never shows a project
// selector.
const GLOBAL_NAV = [
  { id: 'fleet', label: '态势', icon: Radar },
  { id: 'projects', label: '项目', icon: FolderKanban },
  { id: 'kb', label: '世界知识', icon: BookOpen },
  { id: 'vulns', label: '漏洞库', icon: ShieldAlert },
  { id: 'pocs', label: 'POC 库', icon: SquareTerminal },
  { id: 'settings', label: '设置', icon: SettingsIcon },
] as const

type GlobalView = (typeof GLOBAL_NAV)[number]['id']

// One loader per page, used twice: `lazy` renders it and the hover prefetch
// starts it early. Keeping a single definition means the chunk the rail warms
// is always the chunk the view imports.
const PAGE_LOADERS = {
  fleet: () => import('@/views/Fleet').then((m) => ({ default: m.Fleet })),
  projects: () => import('@/views/Projects').then((m) => ({ default: m.Projects })),
  kb: () => import('@/views/Knowledge').then((m) => ({ default: m.Knowledge })),
  vulns: () => import('@/views/Vulns').then((m) => ({ default: m.Vulns })),
  pocs: () => import('@/views/POCs').then((m) => ({ default: m.POCs })),
  settings: () => import('@/views/Settings').then((m) => ({ default: m.SettingsPage })),
} satisfies Record<GlobalView, () => Promise<{ default: ComponentType<any> }>>

// Module scope on purpose: the registry must survive re-renders so a second
// hover on the same row does not start a second download.
const prefetchRegistry = createPrefetchRegistry()
for (const id of Object.keys(PAGE_LOADERS) as GlobalView[]) {
  prefetchRegistry.register(id, PAGE_LOADERS[id])
}

const Fleet = lazy(PAGE_LOADERS.fleet)
const Knowledge = lazy(PAGE_LOADERS.kb)
const POCs = lazy(PAGE_LOADERS.pocs)
const Projects = lazy(PAGE_LOADERS.projects)
const SettingsPage = lazy(PAGE_LOADERS.settings)
const Vulns = lazy(PAGE_LOADERS.vulns)

export function GlobalShell() {
  const location = useConsoleLocation()
  const initialParams = new URLSearchParams(location.search)
  const initial = initialParams.get('view') as GlobalView | null
  // 深链接 /g/?vuln=<id> 也要落到漏洞库：没有 view 时按详情参数推断，
  // 否则分享出去的漏洞详情链接会停在态势页。
  const initialView = GLOBAL_NAV.some((n) => n.id === initial)
    ? (initial as GlobalView)
    : initialParams.get('vuln')
      ? 'vulns'
      : 'fleet'
  const view: GlobalView = initialView
  const projects = useLoad(api.projects, [])
  const list = projects.data ?? []
  const first = list[0]

  // The tab names the instance page. No "全局" prefix: this shell *is* the
  // instance, and the project pages are the ones that need disambiguating.
  useEffect(() => {
    const current = GLOBAL_NAV.find((item) => item.id === view)
    document.title = pageTitle([current?.label])
  }, [view])

  function select(next: GlobalView) {
    navigate(`/g/?view=${next}`)
  }

  return (
    <div className="flex h-full w-full flex-col overflow-hidden lg:flex-row">
      <ConsoleRail title="全局控制台" active={view} footer={first ? (
        <Button size="sm" variant="outline" className="w-full justify-start gap-2" onClick={() => gotoProject(first.id)}>
          <Waypoints className="h-4 w-4" /><span className="truncate">进入项目 · {first.name}</span>
        </Button>
      ) : <div className="px-1 py-1 text-xs text-muted-foreground">还没有项目</div>}>
        <nav aria-label="全局导航" className="min-h-0 flex-1 space-y-1 overflow-y-auto px-3 pb-4">
          {GLOBAL_NAV.map((item) => (
            <SideNav
              key={item.id}
              active={view === item.id}
              icon={item.icon}
              label={item.label}
              onClick={() => select(item.id)}
              // Hovering the row starts the chunk download, so the click swaps
              // content in instead of showing PageSkeleton.
              onIntent={() => prefetchRegistry.prefetch(item.id)}
            />
          ))}
        </nav>
      </ConsoleRail>

      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        {projects.error && <div className="shrink-0 p-4" role="alert"><Callout tone="destructive">项目列表读取失败：{projects.error}</Callout></div>}
        {/* No second title bar here: every global view renders its own banner
            (title + subtitle + actions) through <Page>, so the instance shell
            only supplies navigation. */}
        <div className="min-h-0 flex-1 overflow-hidden">
          <Suspense fallback={<PageSkeleton />}>
            {view === 'fleet' ? (
              <Fleet />
            ) : view === 'projects' ? (
              <Projects projects={list} current="" onChanged={projects.reload} loading={projects.loading} refreshing={projects.refreshing} onDeleted={(id: string) => {
                // Commit the confirmed deletion before revalidating. The reload
                // also retires reads started before the mutation succeeded.
                projects.setData(list.filter(project => project.id !== id))
                projects.reload()
              }} />
            ) : view === 'kb' ? (
              <Knowledge />
            ) : view === 'vulns' ? (
              <Vulns projects={list} />
            ) : view === 'pocs' ? (
              <POCs />
            ) : (
              <SettingsPage />
            )}
          </Suspense>
        </div>
      </div>
    </div>
  )
}
