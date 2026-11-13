import { useState } from 'react'
import { BookOpen, Command, Package, RefreshCw, Search, Terminal, Trophy, Wrench } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Chip,
  EmptyState,
  Input,
  Meter,
  SkeletonList,
  StatTile,
  StatusIndicator,
} from '@cyber/ui'
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

/**
 * Toolbox is a read-only capability directory: three metrics, then the host's
 * registered commands (filterable), then two side-by-side lists (call
 * leaderboard and local tool files). Every block has an explicit empty state —
 * "no host directory" is a state, not a blank panel.
 */
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

  const metrics = [
    { label: '宿主命令与技能', value: connected ? fmtInt(commands.length) : '—', hint: connected ? '来自运行中的默认宿主' : '等待宿主目录', icon: Wrench },
    { label: '项目工具调用', value: data.data ? fmtInt(total) : '—', hint: `${fmtInt(data.data?.runs_scanned ?? 0)} 条运行日志`, icon: Trophy },
    { label: '本机工具文件', value: data.data ? fmtInt(arsenal.length) : '—', hint: '实例与用户 arsenal 目录', icon: Package },
  ]

  return (
    <Page
      title="工具与能力"
      subtitle="查看宿主实际注册的能力、调用活跃度与本机工具文件"
      actions={
        <Button size="sm" variant="outline" disabled={refreshing} aria-label="刷新工具与能力" onClick={refresh}>
          <RefreshCw className={refreshing ? 'h-3.5 w-3.5 animate-spin' : 'h-3.5 w-3.5'} />
          {refreshing ? '刷新中…' : '刷新'}
        </Button>
      }
    >
      <div className="space-y-4">
        {data.error && (
          <Callout tone="destructive">
            {data.error}
            <Button size="xs" variant="outline" className="ml-2" onClick={refresh}>
              重试
            </Button>
          </Callout>
        )}
        {runtime.error && (
          <Callout tone="destructive">
            读取宿主目录失败：{runtime.error}
            <Button size="xs" variant="outline" className="ml-2" onClick={refresh}>
              重试
            </Button>
          </Callout>
        )}

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          {metrics.map(({ label, value, hint, icon: Icon }) => (
            <div key={label} className="tw-panel rounded-xl p-4">
              <StatTile
                label={label}
                value={value}
                hint={hint}
                size="lg"
                icon={<Icon className="h-3.5 w-3.5" />}
              />
            </div>
          ))}
        </div>

        <Panel
          title="宿主能力目录"
          extra={
            <StatusIndicator
              compact
              status={connected ? 'success' : 'idle'}
              label={runtime.loading ? '读取中' : connected ? '已连接' : '未连接'}
            />
          }
        >
          <div className="mb-3 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0 flex-1 text-[12px] leading-relaxed text-ink-2">
              <p>目录反映当前默认宿主的注册结果。模板、指纹、浏览器依赖与外部服务是否可用，仍以实际执行结果为准。</p>
              {connected && (
                <p className="tw-break mt-1 font-mono text-[11px] leading-4 text-ink-3">
                  {runtime.data?.agent_id} · {runtime.data?.provider || '未报告模型提供方'} / {runtime.data?.model || '未报告模型'} · {fmtTime(runtime.data?.checked_at)}
                </p>
              )}
            </div>
            <div className="relative w-full sm:w-64">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" />
              <Input
                className="h-8 pl-7 text-[12px]"
                aria-label="搜索宿主命令与技能"
                placeholder="搜索名称、说明或用法"
                value={query}
                onChange={event => setQuery(event.target.value)}
              />
            </div>
          </div>

          {runtime.data?.error && <div className="mb-3"><Callout tone="warning">{runtime.data.error}</Callout></div>}
          {runtime.data?.model_error && <div className="mb-3"><Callout tone="warning">模型配置异常：{runtime.data.model_error}</Callout></div>}

          <div className="mb-3 flex flex-wrap gap-1.5" role="group" aria-label="能力类别">
            {categories.map(({ id, label, icon: Icon }) => (
              <Chip key={id} active={kind === id} onClick={() => setKind(id)}>
                <Icon className="h-3.5 w-3.5" />
                {label}
                <span className="tw-nums font-mono text-[11px] opacity-70">
                  {commands.filter(command => commandKind(command) === id).length}
                </span>
              </Chip>
            ))}
          </div>

          {runtime.loading ? (
            <SkeletonList count={3} />
          ) : filtered.length > 0 ? (
            <div className="grid gap-2 md:grid-cols-2 2xl:grid-cols-3">
              {filtered.map(command => (
                <article key={command.name} className="tw-inset min-w-0 rounded-lg p-3 transition-colors hover:border-ink-3/30">
                  <h3 className="tw-break font-mono text-[13px] font-semibold leading-5 text-ink">{command.name}</h3>
                  {command.description && (
                    <p className="tw-break mt-1.5 whitespace-pre-wrap text-[12px] leading-relaxed text-ink-2">{command.description}</p>
                  )}
                  {command.usage && (
                    <p className="tw-break mt-2 rounded-md bg-surface-2/70 px-2.5 py-2 font-mono text-[11px] leading-relaxed text-ink-3">{command.usage}</p>
                  )}
                </article>
              ))}
            </div>
          ) : (
            <EmptyState
              icon={Wrench}
              title={query ? '没有匹配的能力' : connected ? '宿主未报告此类能力' : '暂时没有宿主目录'}
              description={
                query
                  ? '尝试其他名称，或切换能力类别。'
                  : connected
                    ? '这里仅展示宿主已注册的项目，不从历史调用推断。'
                    : '宿主连接恢复后刷新，项目调用记录仍可在下方查看。'
              }
              action={query ? <Button size="sm" variant="outline" onClick={() => setQuery('')}>清除搜索</Button> : undefined}
            />
          )}
        </Panel>

        <div className="grid items-start gap-4 xl:grid-cols-3">
          <Panel title={`项目调用排行 · ${rows.length}`} className="xl:col-span-2">
            {data.loading ? (
              <SkeletonList count={4} />
            ) : rows.length === 0 ? (
              <EmptyState icon={Trophy} title="还没有工具调用" description="运行产生工具事件后，这里会显示项目调用记录。" />
            ) : (
              <div className="space-y-3">
                {rows.map((row, index) => (
                  <div key={row.tool} className="flex items-start gap-3">
                    <span className="tw-nums mt-0.5 w-5 shrink-0 text-right font-mono text-[11px] text-ink-3">
                      {String(index + 1).padStart(2, '0')}
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
                        <span className="tw-break font-mono text-[12px] text-ink">{row.tool}</span>
                        <span className="tw-nums shrink-0 font-mono text-[11px] text-ink-3">
                          {fmtInt(row.calls)} 次 · {row.runs} 轮
                        </span>
                      </div>
                      <Meter
                        className="my-1.5"
                        value={Math.max(2, (row.calls / max) * 100)}
                        aria-label={`${row.tool} 调用占比`}
                      />
                      <p className="tw-nums text-[11px] leading-4 text-ink-3">最近调用 {fmtTime(row.last_at)}</p>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Panel>

          <Panel title="本机工具文件" extra={<Badge size="sm" variant="muted">{arsenal.length}</Badge>}>
            <p className="mb-3 text-[12px] leading-relaxed text-ink-2">
              工具由 harness 的 arsenal 管理，跨项目共享，删除项目时保留。文件与版本来自安装目录及清单；文件存在不代表可执行或已进入当前宿主环境。
            </p>
            {data.loading ? (
              <SkeletonList count={3} />
            ) : arsenal.length === 0 ? (
              <EmptyState icon={Package} title="没有检测到工具文件" description="安装到实例或用户 arsenal 目录后刷新查看。" />
            ) : (
              <div className="divide-y divide-hairline">
                {arsenal.map(tool => (
                  <div key={tool.name} className="py-2.5 first:pt-0 last:pb-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="tw-break font-mono text-[12px] font-medium text-ink">{tool.name}</span>
                      {tool.version && <Badge size="sm" variant="secondary">{tool.version}</Badge>}
                      {tool.source && <Badge size="sm" variant="muted">{tool.source}</Badge>}
                      <span className="tw-nums ml-auto font-mono text-[11px] text-ink-3">{fmtBytes(tool.size)}</span>
                    </div>
                    {tool.dirs?.map(dir => (
                      <p key={dir} className="tw-break mt-1 font-mono text-[11px] leading-4 text-ink-3">{dir}</p>
                    ))}
                  </div>
                ))}
              </div>
            )}
          </Panel>
        </div>
      </div>
    </Page>
  )
}
