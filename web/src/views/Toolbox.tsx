import { useState } from 'react'
import { BookOpen, Command, Package, RefreshCw, Search, Terminal, Trophy, Wrench } from 'lucide-react'
import { Badge, Button, Callout, EmptyState, Input, SkeletonList } from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtBytes, fmtInt, fmtTime } from '@/lib/format'
import { useLoad, usePoll } from '@/lib/hooks'
import { commandKind, filterCommands, type CommandKind } from '@/lib/tools'

const categories = [
  { id: 'native', label: '原生命令', icon: Terminal },
  { id: 'skill', label: '技能', icon: BookOpen },
  { id: 'runtime', label: '会话控制', icon: Command },
] as const

export function Toolbox() {
  const data = usePoll(api.tools, 10000)
  const runtime = useLoad(api.runtimeTools, [])
  const [kind, setKind] = useState<CommandKind>('native')
  const [query, setQuery] = useState('')
  const rows = data.data?.leaderboard ?? []
  const arsenal = data.data?.arsenal ?? []
  const commands = runtime.data?.commands ?? []
  const filtered = filterCommands(commands, kind, query)
  const total = rows.reduce((sum, row) => sum + row.calls, 0)
  const max = rows.reduce((value, row) => Math.max(value, row.calls), 1)
  const connected = runtime.data?.state === 'connected'
  const refreshing = runtime.refreshing || data.refreshing
  const refresh = () => { data.reload(); runtime.reload() }

  return (
    <Page title="工具与能力" subtitle="查看宿主实际注册的能力、调用活跃度与本机工具文件" actions={
      <Button size="sm" variant="outline" disabled={refreshing} aria-label="刷新工具与能力" onClick={refresh}>
        <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? 'animate-spin' : ''}`} />{refreshing ? '刷新中…' : '刷新'}
      </Button>
    }>
      <div className="space-y-5">
        {data.error && <Callout tone="destructive">{data.error}</Callout>}
        {runtime.error && <Callout tone="destructive">读取宿主目录失败：{runtime.error}</Callout>}

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          {[
            { label: '宿主命令与技能', value: connected ? fmtInt(commands.length) : '—', hint: connected ? '来自运行中的默认宿主' : '等待宿主目录', icon: Wrench },
            { label: '项目工具调用', value: data.data ? fmtInt(total) : '—', hint: `${data.data?.runs_scanned ?? 0} 条运行日志`, icon: Trophy },
            { label: '本机工具文件', value: data.data ? fmtInt(arsenal.length) : '—', hint: '实例与用户 arsenal 目录', icon: Package },
          ].map(({ label, value, hint, icon: Icon }) => <div key={label} className="rounded-xl border border-border/80 bg-card p-4">
            <div className="flex items-center justify-between text-xs text-muted-foreground"><span>{label}</span><Icon className="h-4 w-4 text-primary/70" /></div>
            <div className="mt-3 font-mono text-2xl font-semibold tracking-tight">{value}</div>
            <p className="mt-1 text-xs text-muted-foreground">{hint}</p>
          </div>)}
        </div>

        <Panel title="宿主能力目录" extra={<Badge variant={connected ? 'success' : 'muted'}>{runtime.loading ? '读取中' : connected ? '已连接' : '未连接'}</Badge>}>
          <div className="mb-4 flex flex-wrap items-start justify-between gap-4">
            <div className="min-w-0 flex-1 text-xs leading-relaxed text-muted-foreground">
              <p>目录反映当前默认宿主的注册结果。模板、指纹、浏览器依赖与外部服务是否可用，仍以实际执行结果为准。</p>
              {connected && <p className="mt-1 break-words font-mono text-[11px]">{runtime.data?.agent_id} · {runtime.data?.provider || '未报告模型提供方'} / {runtime.data?.model || '未报告模型'} · {fmtTime(runtime.data?.checked_at)}</p>}
            </div>
            <div className="relative w-full sm:w-64"><Search className="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" /><Input className="pl-9" aria-label="搜索宿主命令与技能" placeholder="搜索名称、说明或用法" value={query} onChange={event => setQuery(event.target.value)} /></div>
          </div>
          {runtime.data?.error && <div className="mb-4"><Callout tone="warning">{runtime.data.error}</Callout></div>}
          {runtime.data?.model_error && <div className="mb-4"><Callout tone="warning">模型配置异常：{runtime.data.model_error}</Callout></div>}
          <div className="mb-4 flex flex-wrap gap-1.5" role="group" aria-label="能力类别">
            {categories.map(({ id, label, icon: Icon }) => <Button key={id} size="sm" variant={kind === id ? 'soft' : 'ghost'} aria-pressed={kind === id} onClick={() => setKind(id)}>
              <Icon className="h-3.5 w-3.5" />{label}<span className="font-mono text-[11px] opacity-60">{commands.filter(command => commandKind(command) === id).length}</span>
            </Button>)}
          </div>
          {runtime.loading ? <SkeletonList count={3} /> : filtered.length > 0 ? <div className="grid gap-2 md:grid-cols-2 2xl:grid-cols-3">
            {filtered.map(command => <article key={command.name} className="min-w-0 rounded-lg border border-border/70 bg-muted/15 p-3.5">
              <h3 className="break-all font-mono text-[13px] font-semibold text-foreground">{command.name}</h3>
              {command.description && <p className="mt-2 whitespace-pre-wrap break-words text-xs leading-relaxed text-muted-foreground">{command.description}</p>}
              {command.usage && <p className="mt-3 break-all rounded-md bg-muted/60 px-2.5 py-2 font-mono text-[11px] leading-relaxed text-muted-foreground">{command.usage}</p>}
            </article>)}
          </div> : <EmptyState icon={Wrench} title={query ? '没有匹配的能力' : connected ? '宿主未报告此类能力' : '暂时没有宿主目录'} description={query ? '尝试其他名称，或切换能力类别。' : connected ? '这里仅展示宿主已注册的项目，不从历史调用推断。' : '宿主连接恢复后刷新，项目调用记录仍可在下方查看。'} />}
        </Panel>

        <div className="grid items-start gap-4 xl:grid-cols-3">
          <Panel title={`项目调用排行 · ${rows.length}`} className="xl:col-span-2">
            {data.loading ? <SkeletonList count={4} /> : rows.length === 0 ? <EmptyState icon={Trophy} title="还没有工具调用" description="运行产生工具事件后，这里会显示项目调用记录。" /> : <div className="space-y-3">
              {rows.map((row, index) => <div key={row.tool} className="flex items-start gap-3">
                <span className="mt-0.5 w-5 shrink-0 text-right font-mono text-xs text-muted-foreground/60">{String(index + 1).padStart(2, '0')}</span>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs"><span className="break-all font-mono">{row.tool}</span><span className="shrink-0 font-mono text-muted-foreground">{fmtInt(row.calls)} 次 · {row.runs} 轮</span></div>
                  <div className="my-2 h-1.5 overflow-hidden rounded-full bg-muted"><div className="h-full rounded-full bg-primary/70" style={{ width: `${Math.max(2, row.calls / max * 100)}%` }} /></div>
                  <p className="text-[11px] text-muted-foreground">最近调用 {fmtTime(row.last_at)}</p>
                </div>
              </div>)}
            </div>}
          </Panel>
          <Panel title="本机工具文件" extra={<Badge variant="muted">{arsenal.length}</Badge>}>
            <p className="mb-4 text-xs leading-relaxed text-muted-foreground">工具由 harness 的 arsenal 管理，跨项目共享，删除项目时保留。文件与版本来自安装目录及清单；文件存在不代表可执行或已进入当前宿主环境。</p>
            {data.loading ? <SkeletonList count={3} /> : arsenal.length === 0 ? <EmptyState icon={Package} title="没有检测到工具文件" description="安装到实例或用户 arsenal 目录后刷新查看。" /> : <div className="divide-y divide-border/60">
              {arsenal.map(tool => <div key={tool.name} className="py-3 first:pt-0 last:pb-0">
                <div className="flex flex-wrap items-center gap-2"><span className="break-all font-mono text-xs font-medium">{tool.name}</span>{tool.version && <Badge size="sm" variant="secondary">{tool.version}</Badge>}{tool.source && <Badge size="sm" variant="muted">{tool.source}</Badge>}<span className="ml-auto font-mono text-[11px] text-muted-foreground">{fmtBytes(tool.size)}</span></div>
                {tool.dirs?.map(dir => <p key={dir} className="mt-1.5 break-all font-mono text-[10px] leading-relaxed text-muted-foreground">{dir}</p>)}
              </div>)}
            </div>}
          </Panel>
        </div>
      </div>
    </Page>
  )
}
