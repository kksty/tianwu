import { useRef, useState } from 'react'
import { Radar, TriangleAlert } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Field,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Spinner,
  useConfirm,
} from '@cyber/ui'
import { api } from '@/lib/api'
import { nonNegativeInteger } from '@/lib/settings'
import type { ModelCatalogue, ModelProfile } from '@/lib/types'

const PRESETS = [
  { id: 'openai', name: 'OpenAI', protocol: 'openai', base: 'https://api.openai.com/v1' },
  { id: 'opencode-go', name: 'OpenCode Go（订阅）', protocol: 'openai', base: 'https://opencode.ai/inference/go/openai/v1' },
  { id: 'anthropic', name: 'Anthropic', protocol: 'anthropic', base: 'https://api.anthropic.com/v1' },
  { id: 'deepseek', name: 'DeepSeek', protocol: 'openai', base: 'https://api.deepseek.com/v1' },
  { id: 'openrouter', name: 'OpenRouter', protocol: 'openai', base: 'https://openrouter.ai/api/v1' },
  { id: 'groq', name: 'Groq', protocol: 'openai', base: 'https://api.groq.com/openai/v1' },
  { id: 'moonshot', name: 'Moonshot', protocol: 'openai', base: 'https://api.moonshot.cn/v1' },
  { id: 'ollama', name: 'Ollama', protocol: 'openai', base: 'http://localhost:11434/v1' },
  { id: 'zhipu', name: 'Zhipu GLM', protocol: 'openai', base: 'https://open.bigmodel.cn/api/paas/v4' },
]

/**
 * The inline model-profile editor. One "save configuration" action owns the
 * whole form: unlike the old per-field dialogs, the key is only sent on submit,
 * the catalogue read is an explicit secondary action, and the advanced block
 * stays collapsed until something in it was already set.
 */
export function ModelProfileEditor({ profile, active, onSaved, onCancel, onBusy }: {
  profile?: ModelProfile
  /** This profile is the instance's active one: saving it restarts the local host. */
  active?: boolean
  onSaved: (id: string) => void
  onCancel: () => void
  onBusy: (busy: boolean) => void
}) {
  const confirm = useConfirm()
  const [draft, setDraft] = useState({
    id: profile?.id ?? '', name: profile?.name ?? '', provider: profile?.provider || 'openai',
    base_url: profile?.base_url ?? '', model: profile?.model ?? '', api_key: '', proxy: profile?.proxy ?? '',
    context_window: String(profile?.context_window || ''), max_tokens: String(profile?.max_tokens || ''),
    tier: profile?.tier ?? '', max_concurrency: String(profile?.max_concurrency || ''),
  })
  const [preset, setPreset] = useState(() => PRESETS.find(p => p.protocol === draft.provider && p.base === draft.base_url.replace(/\/+$/, ''))?.id ?? 'custom')
  const [busy, setBusy] = useState('')
  const lock = useRef(false)
  const [error, setError] = useState('')
  const [catalogue, setCatalogue] = useState<ModelCatalogue>()
  const [filter, setFilter] = useState('')
  const [advanced, setAdvanced] = useState(Boolean(profile?.tier || profile?.max_concurrency || profile?.proxy || profile?.context_window || profile?.max_tokens))

  function change(key: keyof typeof draft, value: string) {
    if (key === 'base_url' || key === 'api_key' || key === 'provider') setCatalogue(undefined)
    setDraft(current => ({ ...current, [key]: value }))
    setError('')
  }
  async function run(action: string, work: () => Promise<void>) {
    if (lock.current) return
    lock.current = true; setBusy(action); onBusy(true); setError('')
    try { await work() }
    catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { lock.current = false; setBusy(''); onBusy(false) }
  }
  function save() {
    void run('save', async () => {
      const id = draft.id.trim()
      if (!id) throw new Error('配置 ID 不能为空')
      if (!draft.base_url.trim() || !draft.model.trim()) throw new Error('请填写 API 地址与模型 ID')
      let url: URL
      try { url = new URL(draft.base_url.trim()) } catch { throw new Error('API 地址需要完整的 http:// 或 https:// 地址') }
      if (!['http:', 'https:'].includes(url.protocol) || !url.hostname) throw new Error('API 地址只支持 HTTP 或 HTTPS')
      if (!draft.api_key.trim() && !profile?.api_key_set && !profile?.api_key_count) throw new Error('请填写 API Key；无认证的本地服务可按服务要求填写占位凭据')
      // Editing the profile the instance is using changes what the local host
      // must start with, so the server restarts it — and a host restart kills
      // every local AOP session, i.e. the runs in flight on that node fail.
      // That is easy to trigger mid-round (e.g. tuning context_window), so it
      // must be an explicit decision, not a silent side effect of "保存配置".
      if (active && profile && !await confirm({
        title: `保存 ${profile.name || profile.id} 会重启本机宿主`,
        description: '这个配置正在被实例使用：保存后默认本机宿主会按新配置重启，其上正在运行的任务会中断（失败后可重试）。不改动它就不会重启。',
        confirmLabel: '保存并重启',
        cancelLabel: '取消',
        destructive: true,
      })) return
      const body = {
        name: draft.name.trim(), provider: draft.provider.trim(), base_url: draft.base_url.trim(),
        model: draft.model.trim(), api_key: draft.api_key.trim(), proxy: draft.proxy.trim(), tier: draft.tier,
        context_window: nonNegativeInteger(draft.context_window, '上下文窗口'),
        max_tokens: nonNegativeInteger(draft.max_tokens, '单次回复上限'),
        max_concurrency: nonNegativeInteger(draft.max_concurrency, '模型并发'),
      }
      if (profile) await api.updateProfile(profile.id, body)
      else await api.createProfile({ ...body, id })
      onSaved(id)
    })
  }
  function detect() {
    void run('catalogue', async () => {
      setCatalogue(await api.modelCatalogue({ base_url: draft.base_url.trim(), api_key: draft.api_key.trim() || undefined, id: profile?.id }))
    })
  }

  return <form
    onSubmit={e => { e.preventDefault(); save() }}
    className="rounded-xl border border-primary/25 bg-primary/[0.03] p-4 sm:p-5"
    aria-label="模型配置编辑器"
  >
    <div className="mb-4 flex flex-wrap items-center gap-2">
      <h2 className="text-[14px] font-semibold leading-5 text-ink">{profile ? `编辑 ${profile.name || profile.id}` : '新增模型配置'}</h2>
      <Badge variant="outline" size="sm">统一保存</Badge>
    </div>
    <fieldset disabled={Boolean(busy)} className="min-w-0 space-y-4">
      <div className="grid min-w-0 gap-4 md:grid-cols-2">
        <Field label="配置 ID" hint={profile ? '稳定标识，创建后不可修改' : '例如 main-model；任务和项目通过它引用配置'}>
          <Input aria-label="配置 ID" value={draft.id} disabled={Boolean(profile)} onChange={e => change('id', e.target.value)} placeholder="main-model" autoComplete="off" className="font-mono text-[12px]" />
        </Field>
        <Field label="显示名称" hint="只用于展示，不改变任务引用">
          <Input aria-label="显示名称" value={draft.name} onChange={e => change('name', e.target.value)} placeholder="例如 日常模型" />
        </Field>
        <Field label="供应商预设" hint="填入协议与地址；也支持自定义网关">
          <Select value={preset} disabled={Boolean(busy)} onValueChange={value => {
            setPreset(value); setCatalogue(undefined)
            const selected = PRESETS.find(p => p.id === value)
            if (selected) setDraft(current => ({ ...current, provider: selected.protocol, base_url: selected.base }))
          }}><SelectTrigger aria-label="供应商预设"><SelectValue /></SelectTrigger><SelectContent>
            {PRESETS.map(p => <SelectItem key={p.id} value={p.id}>{p.name}</SelectItem>)}
            <SelectItem value="custom">自定义网关</SelectItem>
          </SelectContent></Select>
        </Field>
        <Field label="API 协议" hint="选择服务端实际支持的协议">
          <Select value={draft.provider} disabled={Boolean(busy)} onValueChange={value => { setPreset('custom'); change('provider', value) }}>
            <SelectTrigger aria-label="API 协议"><SelectValue /></SelectTrigger><SelectContent>
              <SelectItem value="openai">OpenAI 兼容</SelectItem><SelectItem value="anthropic">Anthropic</SelectItem>
              {!['openai', 'anthropic'].includes(draft.provider) && <SelectItem value={draft.provider}>{draft.provider}</SelectItem>}
            </SelectContent>
          </Select>
        </Field>
        <Field label="API 地址" hint="完整 Base URL；OpenCode Go 请选订阅预设，保留 /inference/go/openai/v1">
          <Input aria-label="API 地址" value={draft.base_url} onChange={e => { setPreset('custom'); change('base_url', e.target.value) }} placeholder="https://gateway.example/v1" className="font-mono text-[12px]" />
        </Field>
        <Field label="API Key" hint={profile ? '留空保留已保存凭据；点击保存才会提交' : '仅保存时提交，失焦不会自动创建配置'}>
          <Input aria-label="API Key" type="password" value={draft.api_key} onChange={e => change('api_key', e.target.value)} placeholder={profile?.api_key_set ? '已保存，留空不改' : '填写服务凭据'} autoComplete="new-password" className="font-mono text-[12px]" />
        </Field>
        <div className="min-w-0 md:col-span-2">
          <Field label="模型 ID" hint="可手填，或读取端点模型目录后选择；目录检测不执行模型推理">
            <div className="flex flex-wrap gap-2">
              <Input aria-label="模型 ID" value={draft.model} onChange={e => change('model', e.target.value)} placeholder="服务端实际提供的模型 ID" className="min-w-0 flex-1 font-mono text-[12px]" />
              <Button type="button" variant="outline" size="sm" disabled={Boolean(busy) || !draft.base_url.trim()} onClick={detect} aria-busy={busy === 'catalogue'}>
                <Radar className="h-3.5 w-3.5" />{busy === 'catalogue' ? '读取中…' : '读取模型目录'}
              </Button>
            </div>
          </Field>
        </div>
      </div>

      {catalogue && (
        <div className="space-y-2 rounded-lg border border-hairline bg-surface-2/40 p-3">
          <div className="flex flex-wrap items-center gap-2 text-[12px]">
            <span className="font-medium text-ink">端点模型目录</span>
            <Badge variant="muted" size="sm">{catalogue.models.length} 个</Badge>
            <span className="tw-break min-w-0 font-mono text-[11px] text-ink-3">{catalogue.endpoint}</span>
          </div>
          {catalogue.error && <Callout tone="warning">{catalogue.error}</Callout>}
          {catalogue.models.length > 0 ? <>
            <Input aria-label="筛选目录模型" value={filter} onChange={e => setFilter(e.target.value)} placeholder="筛选模型 ID" className="h-8 text-[12px]" />
            <div className="flex max-h-40 flex-wrap gap-1.5 overflow-y-auto">
              {catalogue.models.filter(model => model.toLowerCase().includes(filter.toLowerCase())).map(model => (
                <Button key={model} type="button" size="xs" variant={draft.model === model ? 'secondary' : 'outline'} onClick={() => change('model', model)} className="max-w-full whitespace-normal break-all font-mono">
                  {model}
                </Button>
              ))}
            </div>
            {draft.model && !catalogue.models.includes(draft.model.trim()) && <div className="text-[11px] text-warning">当前模型 ID 不在返回的目录中，请核对。</div>}
          </> : !catalogue.error && <div className="text-[11px] text-ink-3">目录为空，可手动填写模型 ID。</div>}
        </div>
      )}

      <Button type="button" variant="ghost" size="sm" aria-expanded={advanced} onClick={() => setAdvanced(value => !value)}>
        {advanced ? '收起高级配置' : '展开高级配置'}
      </Button>
      {advanced && (
        <div className="grid min-w-0 gap-4 rounded-lg border border-hairline bg-surface-2/30 p-3 sm:grid-cols-2">
          <Field label="上下文窗口" hint="token 数；留空或 0 由 harness 判断">
            <Input aria-label="上下文窗口" type="number" min={0} step={1} value={draft.context_window} onChange={e => change('context_window', e.target.value)} placeholder="自动" />
          </Field>
          <Field label="单次回复上限" hint="token 数；留空或 0 使用 harness 默认">
            <Input aria-label="单次回复上限" type="number" min={0} step={1} value={draft.max_tokens} onChange={e => change('max_tokens', e.target.value)} placeholder="默认" />
          </Field>
          <Field label="执行者并发容量" hint="所有项目共用此配置的执行者数量；0 表示不限">
            <Input aria-label="执行者并发容量" type="number" min={0} step={1} value={draft.max_concurrency} onChange={e => change('max_concurrency', e.target.value)} placeholder="不限" />
          </Field>
          <Field label="资源标注（可选）" hint="兼容现有 tier 路由；不决定职责、模型推理强度或委派权限">
            <Select value={draft.tier || 'none'} disabled={Boolean(busy)} onValueChange={value => change('tier', value === 'none' ? '' : value)}>
              <SelectTrigger aria-label="资源标注"><SelectValue /></SelectTrigger><SelectContent>
                <SelectItem value="none">不标注（单模型也适用）</SelectItem><SelectItem value="strong">strong · 资源标签</SelectItem><SelectItem value="worker">worker · 资源标签</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <div className="min-w-0 sm:col-span-2">
            <Field label="模型请求代理" hint="沿用已有 proxy 配置；留空使用宿主默认网络设置">
              <Input aria-label="模型请求代理" value={draft.proxy} onChange={e => change('proxy', e.target.value)} placeholder="可选代理地址" className="font-mono text-[12px]" />
            </Field>
          </div>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2 border-t border-hairline pt-4">
        <Button type="submit" size="sm" disabled={Boolean(busy)} aria-busy={busy === 'save'}>
          {busy === 'save' && <Spinner size="sm" className="h-3.5 w-3.5" />}
          {busy === 'save' ? '保存中…' : '保存配置'}
        </Button>
        <Button type="button" size="sm" variant="outline" disabled={Boolean(busy)} onClick={onCancel}>取消编辑</Button>
        <span className="text-[11px] text-ink-3">保存不切换实例默认；其他配置的已有运行保留原绑定。保存正在使用的配置会重启本机宿主，运行中的任务会中断。</span>
      </div>
    </fieldset>
    <div role="status" className="mt-3 space-y-2">
      {error && <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>保存失败：{error}</Callout>}
    </div>
  </form>
}
