import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Bot,
  Check,
  FileText,
  FolderKanban,
  Pause,
  Pencil,
  Play,
  Plus,
  Radar,
  RefreshCw,
  Settings2,
  Trash2,
  TriangleAlert,
} from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  EmptyState,
  Field,
  Input,
  SkeletonList,
  Spinner,
  StatTile,
  useConfirm,
} from '@cyber/ui'
import { cn } from '@cyber/theme'
import { Page, Panel } from '@/components/Page'
import { ModelProfileEditor } from '@/components/ModelProfileEditor'
import { ProjectSettingsCard } from '@/components/ProjectSettingsCard'
import { api } from '@/lib/api'
import { useLoad, type AsyncState } from '@/lib/hooks'
import { profileStatus } from '@/lib/settings'
import type { ModelProfile, RuntimeSettings } from '@/lib/types'

const SECTIONS = [
  { id: 'models', label: '模型配置', icon: Bot },
  { id: 'projects', label: '项目默认', icon: Settings2 },
  { id: 'reference', label: '配置说明', icon: FileText },
] as const

/** Profiles describe resources; project defaults choose their use, independently of roles. */
export function SettingsPage() {
  const settings = useLoad(api.settings, [])
  const index = useLoad(api.projectIndex, [])
  const catalogue = useLoad(api.profiles, [])
  // The runtime gate is an instance setting like the rest of this page: it is
  // loaded here (not inside its panel) so the page's 刷新 reloads it too.
  const runtime = useLoad(api.runtimeSettings, [])
  const confirm = useConfirm()
  const [section, setSection] = useState<(typeof SECTIONS)[number]['id']>('models')
  const [editing, setEditing] = useState<ModelProfile | 'new' | null>(null)
  const [editorBusy, setEditorBusy] = useState(false)
  const [action, setAction] = useState('')
  const lock = useRef(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState<{ text: string; tone: 'success' | 'warning' | 'info' }>()
  const [dirtyProjects, setDirtyProjects] = useState<Record<string, boolean>>({})
  const [modelFilter, setModelFilter] = useState('')
  const [projectFilter, setProjectFilter] = useState('')
  const busy = Boolean(action) || editorBusy
  const profiles = catalogue.data?.profiles ?? []
  const active = catalogue.data?.active_profile || settings.data?.active_profile || ''
  const defaultProfile = profiles.find(p => p.id === active)
  const dirtyCount = Object.values(dirtyProjects).filter(Boolean).length
  const hasUnsaved = editing !== null || dirtyCount > 0
  const unsavedCount = dirtyCount + (editing ? 1 : 0)
  const refreshing = settings.refreshing || catalogue.refreshing || index.refreshing || runtime.refreshing
  const onDirty = useCallback((id: string, dirty: boolean) => setDirtyProjects(current => current[id] === dirty ? current : { ...current, [id]: dirty }), [])

  useEffect(() => {
    if (!hasUnsaved) return
    const prevent = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = '' }
    window.addEventListener('beforeunload', prevent)
    return () => window.removeEventListener('beforeunload', prevent)
  }, [hasUnsaved])

  function refresh() { settings.reload(); catalogue.reload(); index.reload(); runtime.reload() }
  async function run(id: string, work: () => Promise<void>) {
    if (lock.current || editorBusy) return
    lock.current = true; setAction(id); setError(''); setNotice(undefined)
    try { await work() } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { lock.current = false; setAction('') }
  }
  async function beginEdit(profile: ModelProfile | 'new') {
    if (busy) return
    if (editing && !await confirm({ title: '放弃当前模型草稿？', description: '当前编辑内容尚未保存。', confirmLabel: '放弃并切换' })) return
    setError(''); setNotice(undefined); setEditing(profile)
  }
  function saved(id: string) {
    setEditing(null)
    setNotice({ tone: 'success', text: `已保存 ${id}。可点击“检测目录”核对模型 ID，或将它选为项目使用的配置。` })
    catalogue.reload(); settings.reload()
  }
  function probe(profile: ModelProfile) {
    void run(`probe:${profile.id}`, async () => {
      const result = await api.probeProfile(profile.id)
      const status = profileStatus({ ...profile, ...result })
      setNotice({ tone: result.probed && result.available ? 'success' : 'warning', text: `${profile.name || profile.id}：${status.label}。${status.detail}` })
      catalogue.reload()
    })
  }
  function activate(profile: ModelProfile) {
    void run(`activate:${profile.id}`, async () => {
      if (!await confirm({ title: `将 ${profile.name || profile.id} 设为实例默认？`, description: '默认本机宿主将重新启动，其上正在运行的任务会中断。跟随实例默认的指挥官和执行者在后续派发时使用此配置；其他命名配置的宿主不受影响。', confirmLabel: '设为默认' })) return
      await api.activateProfile(profile.id)
      setNotice({ tone: 'success', text: `${profile.id} 已设为实例默认。` })
      catalogue.reload(); settings.reload()
    })
  }
  function pause(profile: ModelProfile) {
    void run(`pause:${profile.id}`, async () => {
      const paused = !profile.paused
      await api.pauseProfile(profile.id, paused)
      setNotice({ tone: 'success', text: paused
        ? `${profile.name || profile.id} 已暂停。当前运行继续；使用它作为默认模型的新运行会自动选择其他可用模型，恢复后重新使用原默认。`
        : `${profile.name || profile.id} 已恢复使用。` })
      catalogue.reload(); settings.reload()
    })
  }
  function remove(profile: ModelProfile) {
    void run(`delete:${profile.id}`, async () => {
      const references = index.data?.projects.filter(p => p.worker_profile === profile.id || p.strong_profile === profile.id) ?? []
      if (references.length) throw new Error(`配置仍被项目引用：${references.map(p => p.name).join('、')}。请先在“项目默认”切换配置，再删除。`)
      if (!index.data) throw new Error('项目引用尚未加载，暂不能删除配置。请刷新后重试。')
      if (!await confirm({ title: `删除 ${profile.name || profile.id}？`, description: '历史运行的模型快照保持不变。意图仍可能显式引用此 ID，请确认后续不再使用。', confirmLabel: '删除配置', destructive: true })) return
      await api.deleteProfile(profile.id)
      setNotice({ tone: 'success', text: `已删除 ${profile.id}。` })
      catalogue.reload(); settings.reload()
    })
  }

  const visibleModels = profiles.filter(p => [p.id, p.name, p.model, p.base_url].some(value => value?.toLowerCase().includes(modelFilter.toLowerCase())))
  const projectIndex = index.data
  const projects = projectIndex?.projects ?? []
  const runnable = profiles.filter(p => p.runnable && !(p.probed && !p.available)).length

  return (
    <Page
      title="设置"
      subtitle="管理模型资源、项目默认与运行选项。一个模型配置即可承担全部职责。"
      actions={
        <Button
          size="sm"
          variant="outline"
          disabled={busy || refreshing}
          aria-busy={refreshing}
          onClick={refresh}
        >
          <RefreshCw className={cn('h-3.5 w-3.5', refreshing && 'motion-safe:animate-spin')} />
          {refreshing ? '刷新中…' : '刷新'}
        </Button>
      }
    >
      <div className="mx-auto max-w-[1100px] space-y-5">
        {settings.error && (
          <div role="alert">
            <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                <span className="min-w-0 tw-break">读取实例设置失败：{settings.error}</span>
                <Button size="xs" variant="outline" disabled={refreshing} onClick={settings.reload}>重试</Button>
              </div>
            </Callout>
          </div>
        )}
        {settings.data?.harness_error && (
          <div role="alert"><Callout tone="destructive">模型配置文件无法读取：{settings.data.harness_error}</Callout></div>
        )}

        {/* Summary first: which model the instance runs on, then how many
            resources and projects exist, then whether anything is unsaved. */}
        <div className="tw-panel rounded-xl p-4 sm:p-5">
          <div className="grid min-w-0 grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-5">
            <div className="min-w-0 sm:col-span-2">
              <div className="text-[11px] font-medium leading-4 text-ink-3">实例默认模型</div>
              <div className="mt-1.5 truncate text-[14px] font-semibold leading-5 text-ink" title={defaultProfile?.name || active || settings.data?.model}>
                {defaultProfile?.name || active || settings.data?.model || (settings.loading ? '加载中…' : '尚未配置')}
              </div>
              <div className="mt-1 truncate font-mono text-[11px] leading-4 text-ink-3">
                {defaultProfile?.model || (active ? active : '添加配置后设为默认')}
              </div>
            </div>
            <StatTile
              label="模型配置"
              value={catalogue.loading ? '…' : profiles.length}
              hint={`${runnable} 个具备派发条件`}
            />
            <StatTile
              label="项目"
              value={index.loading ? '…' : projects.length}
              hint={dirtyCount ? `${dirtyCount} 个有未保存改动` : '模型、并发、复核与时限'}
              tone={dirtyCount ? 'warning' : 'default'}
            />
            <StatTile
              label="未保存改动"
              value={unsavedCount}
              tone={unsavedCount ? 'warning' : 'muted'}
              hint={hasUnsaved ? '保存或撤销后再离开' : '没有待保存的草稿'}
            />
          </div>
        </div>

        <div className="flex flex-wrap gap-1 rounded-lg border border-hairline bg-surface-2/60 p-1" role="tablist" aria-label="设置分类">
          {SECTIONS.map((item, position) => (
            <button
              key={item.id}
              type="button"
              role="tab"
              id={`settings-tab-${item.id}`}
              aria-selected={section === item.id}
              aria-controls={`settings-panel-${item.id}`}
              tabIndex={section === item.id ? 0 : -1}
              onKeyDown={event => {
                const next = event.key === 'ArrowRight' ? (position + 1) % SECTIONS.length : event.key === 'ArrowLeft' ? (position + SECTIONS.length - 1) % SECTIONS.length : event.key === 'Home' ? 0 : event.key === 'End' ? SECTIONS.length - 1 : -1
                if (next < 0) return
                event.preventDefault(); setSection(SECTIONS[next].id); document.getElementById(`settings-tab-${SECTIONS[next].id}`)?.focus()
              }}
              onClick={() => setSection(item.id)}
              className={cn(
                'flex h-8 items-center gap-1.5 rounded-md px-2.5 text-[12px] transition-colors',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50',
                section === item.id
                  ? 'bg-surface-3 font-medium text-ink shadow-soft'
                  : 'text-ink-2 hover:bg-surface-3/60 hover:text-ink',
              )}
            >
              <item.icon className="h-3.5 w-3.5" />
              {item.label}
              {item.id === 'projects' && dirtyCount > 0 && (
                <Badge variant="warning" size="sm" className="tw-nums">{dirtyCount}</Badge>
              )}
            </button>
          ))}
        </div>

        <div hidden={section !== 'models'} role="tabpanel" id="settings-panel-models" aria-labelledby="settings-tab-models" className="space-y-4">
          <Callout tone="info">只用一种模型时，设为实例默认并让项目跟随即可。多个配置用于不同端点、模型或容量需求；无需配置“小模型”，资源标注也不会改变模型的推理强度。</Callout>
          <Panel
            title="模型配置"
            extra={
              editing
                ? <Badge variant="outline" size="sm">编辑中</Badge>
                : (
                  <Button size="sm" disabled={busy || !catalogue.data} onClick={() => void beginEdit('new')}>
                    <Plus className="h-3.5 w-3.5" />新增配置
                  </Button>
                )
            }
            bodyClassName="space-y-4"
          >
            {editing && (
              <ModelProfileEditor
                key={editing === 'new' ? '__new' : editing.id}
                profile={editing === 'new' ? undefined : editing}
                active={editing !== 'new' && editing.id === active}
                onSaved={saved}
                onCancel={() => setEditing(null)}
                onBusy={setEditorBusy}
              />
            )}
            {profiles.length > 3 && (
              <Input
                aria-label="筛选模型配置"
                value={modelFilter}
                onChange={e => setModelFilter(e.target.value)}
                placeholder="按名称、模型或 API 地址筛选"
                className="h-8 max-w-sm text-[12px]"
              />
            )}
            {catalogue.error && (
              <div role="alert">
                <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                    <span className="min-w-0 tw-break">读取模型配置失败：{catalogue.error}</span>
                    <Button size="xs" variant="outline" disabled={catalogue.loading} onClick={catalogue.reload}>重试</Button>
                  </div>
                </Callout>
              </div>
            )}
            {catalogue.loading && profiles.length === 0 && <SkeletonList count={2} />}
            {!catalogue.loading && !catalogue.error && !profiles.length && (
              <EmptyState
                icon={Bot}
                title="添加第一个模型配置"
                description="填写端点、模型 ID 与凭据，即可供指挥官和执行者共用。"
                action={<Button size="sm" variant="outline" disabled={busy || !catalogue.data} onClick={() => void beginEdit('new')}><Plus className="h-3.5 w-3.5" />新增配置</Button>}
              />
            )}
            <div className="divide-y divide-hairline">
              {visibleModels.map(profile => {
                const status = profileStatus(profile)
                const unavailable = !profile.runnable || (profile.probed && !profile.available)
                const references = projects.filter(p => p.worker_profile === profile.id || p.strong_profile === profile.id)
                const probing = action === `probe:${profile.id}`
                return (
                  <article
                    key={profile.id}
                    className={cn(
                      'min-w-0 px-3 py-3.5 transition-colors',
                      profile.active
                        ? 'bg-primary/[0.05] shadow-[inset_2px_0_0_var(--primary)]'
                        : 'hover:bg-surface-2/50',
                    )}
                  >
                    <div className="flex flex-wrap items-start gap-3">
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <h3 className="break-all text-[13px] font-semibold leading-5 text-ink">{profile.name || profile.id}</h3>
                          {profile.active && <Badge variant="secondary" size="sm"><Check className="mr-0.5 h-3.5 w-3.5" />实例默认</Badge>}
                          <Badge variant={status.tone} size="sm" title={status.detail}>{status.label}</Badge>
                          {profile.local && <Badge variant="outline" size="sm">本地／私网</Badge>}
                        </div>
                        <div className="mt-1.5 break-all font-mono text-[12px] leading-4 text-ink-2">{profile.model || '未填写模型 ID'}</div>
                        <div className="mt-0.5 break-all font-mono text-[11px] leading-4 text-ink-3">{profile.base_url || '未填写 API 地址'}</div>
                      </div>
                      <div className="flex max-w-full flex-wrap items-center gap-1.5">
                        <Button
                          size="xs"
                          variant="outline"
                          disabled={busy || editing !== null || profile.paused}
                          onClick={() => probe(profile)}
                          title="发送一次简短推理请求，检查 API 与 Key 是否能取得有效回复；不验证工具调用"
                          aria-busy={probing}
                        >
                          {probing ? <Spinner size="sm" className="h-3.5 w-3.5" /> : <Radar className="h-3.5 w-3.5" />}
                          {probing ? '检测中…' : '连通性检测'}
                        </Button>
                        <Button
                          size="xs"
                          variant="outline"
                          disabled={busy || editing !== null}
                          onClick={() => pause(profile)}
                          aria-label={`${profile.paused ? '恢复使用' : '暂停使用'} ${profile.id}`}
                          aria-busy={action === `pause:${profile.id}`}
                        >
                          {profile.paused ? <Play className="h-3.5 w-3.5" /> : <Pause className="h-3.5 w-3.5" />}
                          {action === `pause:${profile.id}` ? '保存中…' : profile.paused ? '恢复使用' : '暂停使用'}
                        </Button>
                        <Button size="xs" variant="ghost" disabled={busy} onClick={() => void beginEdit(profile)} aria-label={`编辑 ${profile.id}`}>
                          <Pencil className="h-3.5 w-3.5" />编辑
                        </Button>
                        {!profile.active && (
                          <Button
                            size="xs"
                            variant="ghost"
                            disabled={busy || editing !== null || unavailable || Boolean(catalogue.data?.has_overrides)}
                            onClick={() => activate(profile)}
                            title={catalogue.data?.has_overrides ? '配置文件中的平铺覆盖字段阻止切换' : unavailable ? status.detail : '重启默认本机宿主'}
                          >
                            {action === `activate:${profile.id}` ? <Spinner size="sm" className="h-3.5 w-3.5" /> : null}
                            设为默认
                          </Button>
                        )}
                        <Button
                          size="xs"
                          variant="ghost"
                          className="text-ink-2 hover:bg-destructive/10 hover:text-destructive"
                          disabled={busy || profile.active || !index.data || editing !== null}
                          onClick={() => remove(profile)}
                          aria-label={`删除 ${profile.id}`}
                          title={profile.active ? '实例默认配置不能删除' : undefined}
                          aria-busy={action === `delete:${profile.id}`}
                        >
                          {action === `delete:${profile.id}` ? <Spinner size="sm" className="h-3.5 w-3.5" /> : <Trash2 className="h-3.5 w-3.5" />}
                        </Button>
                      </div>
                    </div>
                    <div className="mt-2.5 flex flex-wrap gap-x-4 gap-y-1.5 text-[11px] leading-4 text-ink-3">
                      <span className="break-all font-mono">ID · {profile.id}</span>
                      <span>{profile.provider || '默认协议'}</span>
                      <span>{profile.api_key_set || profile.api_key_count ? `凭据已保存${profile.api_key_count && profile.api_key_count > 1 ? ` · ${profile.api_key_count} 把` : ''}` : '未配置凭据'}</span>
                      <span>执行者容量 · {profile.max_concurrency || '不限'}</span>
                      {profile.context_window ? <span className="tw-nums">上下文 · {profile.context_window.toLocaleString()} token</span> : null}
                      {profile.tier && <span>资源标注 · {profile.tier}</span>}
                      {references.length > 0 && <span>项目引用 · {references.length}</span>}
                    </div>
                    {profile.probe_error && (
                      <div className="mt-2 break-words rounded-md bg-surface-2 p-2 text-[11px] leading-4 text-ink-3 [overflow-wrap:anywhere]">{status.detail}</div>
                    )}
                  </article>
                )
              })}
            </div>
            {modelFilter && !visibleModels.length && <EmptyState compact icon={Bot} title="没有匹配的模型配置" description="换个名称、模型 ID 或 API 地址关键词。" />}
            <div aria-live="polite" className="space-y-2">
              {notice && <Callout tone={notice.tone}>{notice.text}</Callout>}
              {error && <Callout tone="destructive">{error}</Callout>}
            </div>
          </Panel>
          <RuntimeConcurrencyPanel runtime={runtime} />
        </div>

        <div hidden={section !== 'projects'} role="tabpanel" id="settings-panel-projects" aria-labelledby="settings-tab-projects" className="space-y-4">
          <Callout tone="info">指挥官与执行者可选同一配置。每个项目独立保存，留空表示跟随实例；这些设置不规定分工或渗透流程。并发仅计算执行者，指挥官、复核和报告不占该名额；所有项目固定 verified_only：成功意图先进入固定复核，新的 vuln/cred 结论通过后才完成，未通过进入 review_failed 可原地重试；旧 standard 清单在加载时自动迁移。</Callout>
          {index.error && (
            <div role="alert">
              <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                  <span className="min-w-0 tw-break">读取项目设置失败：{index.error}</span>
                  <Button size="xs" variant="outline" disabled={index.loading} onClick={index.reload}>重试</Button>
                </div>
              </Callout>
            </div>
          )}
          {catalogue.error && <Callout tone="warning">读取模型列表失败：{catalogue.error}</Callout>}
          {index.loading && projects.length === 0 && <SkeletonList count={2} />}
          {!index.loading && !index.error && !projects.length && (
            <div className="tw-panel rounded-xl">
              <EmptyState
                icon={FolderKanban}
                title="暂无项目"
                description="创建项目后，可在这里设置默认模型与运行选项。"
                action={<Button size="sm" variant="outline" onClick={() => window.location.assign('/g/?view=projects')}>去新建项目</Button>}
              />
            </div>
          )}
          {projects.length > 3 && (
            <Input
              aria-label="筛选项目设置"
              value={projectFilter}
              onChange={e => setProjectFilter(e.target.value)}
              placeholder="按项目名称或 ID 筛选"
              className="h-8 max-w-sm text-[12px]"
            />
          )}
          {projectIndex && projects.map(project => <div key={project.id} hidden={Boolean(projectFilter) && !`${project.name} ${project.id}`.toLowerCase().includes(projectFilter.toLowerCase())}>
            <ProjectSettingsCard project={project} profiles={profiles} active={active} defaults={projectIndex} onSaved={index.reload} onDirty={onDirty} />
          </div>)}
        </div>

        <div hidden={section !== 'reference'} role="tabpanel" id="settings-panel-reference" aria-labelledby="settings-tab-reference" className="space-y-4">
          <Panel title="配置位置与生效范围" bodyClassName="space-y-4">
            <div>
              <div className="text-[12px] font-medium text-ink">配置文件</div>
              <div className="tw-inset tw-break mt-1.5 rounded-lg px-3 py-2 font-mono text-[11px] text-ink-2">{settings.data?.path || catalogue.data?.path || '加载中…'}</div>
            </div>
            {catalogue.data?.has_overrides && <Callout tone="warning">检测到 llm 下的平铺覆盖字段。它们可能遮盖命名配置，后端会拒绝切换默认。请检查配置文件并显式处理覆盖字段。</Callout>}
            <dl className="space-y-3.5">
              {[
                ['模型配置', '保存其他配置只影响后续选择，当前运行保留原绑定；保存正在使用的配置会重启默认本机宿主，确认框会说明影响（运行中的任务会中断，失败后可重试）。更改实例默认同样重启。远端 AOP 节点使用自己的宿主模型配置。'],
                ['项目默认', '指挥官模型对应 strong_profile，执行者默认模型对应 worker_profile。两个字段可指向同一配置。显式指定模型的意图优先使用自己的配置。'],
                ['运行选项', '并发调整只影响后续派发；单轮时限仅约束指挥官当轮决策，不是项目总时长，也不是费用控制。暂停与目标完成后的休眠继续由运行机制负责。'],
                ['高级资源标注', 'tier 的 strong／worker 为兼容现有路由保留，不是固定角色，不代表模型 reasoning effort。角色由任务决定，无需凑齐层级。'],
                ['稳定 ID 与凭据', '配置 ID 创建后不可改。需要换 ID 时先新建配置，再调整项目和意图引用。页面不读取已保存的密钥；编辑时 Key 留空保留原值。密钥池、凭据失效后的单跳 fallback 及宿主专有字段继续保留在配置文件中。'],
              ].map(([term, description]) => (
                <div key={term}>
                  <dt className="text-[12px] font-medium text-ink">{term}</dt>
                  <dd className="mt-0.5 text-[12px] leading-5 text-ink-2">{description}</dd>
                </div>
              ))}
            </dl>
          </Panel>
        </div>
      </div>
    </Page>
  )
}

/**
 * The verifier and the report writer share one instance-level model gate. Its
 * concurrency was hard-coded to 1 (serial); the operator can raise it here.
 * In-flight auxiliary runs keep their slot; a change applies to new calls.
 */
function RuntimeConcurrencyPanel({ runtime }: { runtime: AsyncState<RuntimeSettings> }) {
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  useEffect(() => {
    if (runtime.data) setDraft(String(runtime.data.auxiliary_concurrency))
  }, [runtime.data])

  const current = runtime.data?.auxiliary_concurrency
  const max = runtime.data?.max_auxiliary_concurrency ?? 8
  const dirty = runtime.data != null && draft !== String(current)

  async function save() {
    const value = Number(draft)
    if (!Number.isInteger(value) || value < 1 || value > max) {
      setError(`并发必须是 1–${max} 的整数`)
      return
    }
    setBusy(true); setError(''); setNotice('')
    try {
      const next = await api.saveRuntimeSettings(value)
      runtime.setData(next)
      setDraft(String(next.auxiliary_concurrency))
      setNotice('已保存：之后发起的复核/报告按新并发执行，已在跑的不受影响。')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Panel
      title="复核与报告并发"
      extra={<Badge variant="outline" size="sm">实例级</Badge>}
      bodyClassName="space-y-3"
    >
      <div className="grid min-w-0 gap-4 sm:grid-cols-[minmax(0,220px)_1fr] sm:items-end">
        <Field
          label="同时运行的复核/报告数"
          hint={runtime.loading ? '加载中…' : `当前 ${current ?? '…'}，可设 1–${max}`}
        >
          <Input
            type="number"
            min={1}
            max={max}
            step={1}
            value={draft}
            onChange={e => setDraft(e.target.value)}
            disabled={busy || !runtime.data}
            aria-label="复核与报告并发"
          />
        </Field>
        <div className="flex flex-wrap items-center gap-2 pb-0.5">
          <Button size="sm" variant="secondary" onClick={() => void save()} disabled={busy || !runtime.data || !dirty} aria-busy={busy}>
            {busy && <Spinner size="sm" className="h-3.5 w-3.5" />}
            {busy ? '保存中…' : '保存并发'}
          </Button>
          {runtime.error && <Button size="xs" variant="outline" onClick={runtime.reload}>重试读取</Button>}
        </div>
      </div>
      <p className="text-[11px] leading-5 text-ink-3">
        复核与报告共享这道实例级闸门；1 = 串行（默认，最稳）。调大只影响之后的模型调用，已在跑的复核/报告不受影响，也不占执行者并发名额。
      </p>
      <div aria-live="polite" className="space-y-2">
        {notice && <Callout tone="success">{notice}</Callout>}
        {error && <Callout tone="destructive">保存失败：{error}</Callout>}
        {runtime.error && <Callout tone="destructive">读取复核并发失败：{runtime.error}</Callout>}
      </div>
    </Panel>
  )
}
