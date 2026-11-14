import { useEffect, useRef, useState } from 'react'
import { Badge, Button, Callout, Field, Input, Select, SelectContent, SelectItem, SelectTrigger, SelectValue, Spinner, Textarea } from '@cyber/ui'
import { Panel } from './Page'
import { api } from '@/lib/api'
import { DEFAULT_PROJECT_CONSTRAINTS } from '@/lib/project-defaults'
import { durationLabel, mergeProjectSettings, projectSettingsDraft, projectSettingsPatch } from '@/lib/settings'
import type { ModelProfile, ProjectInfo } from '@/lib/types'

const INHERIT = '__inherit'

/**
 * One project's defaults. Every field is optional: an empty value means "follow
 * the instance", and the effective value is spelled out in the hint so the
 * operator never has to guess what a blank will do at dispatch time.
 */
export function ProjectSettingsCard({ project, profiles, active, defaults, onSaved, onDirty }: {
  project: ProjectInfo
  profiles: ModelProfile[]
  active: string
  defaults: { max_runs: number; round_timeout_seconds: number; verify_auto_default: boolean }
  onSaved: () => void
  onDirty: (id: string, dirty: boolean) => void
}) {
  const baseline = useRef(projectSettingsDraft(project))
  const [draft, setDraft] = useState(baseline.current)
  const [busy, setBusy] = useState(false)
  const lock = useRef(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const dirty = Object.keys(draft).some(key => draft[key as keyof typeof draft] !== baseline.current[key as keyof typeof draft])

  useEffect(() => {
    const incoming = projectSettingsDraft(project)
    const previous = baseline.current
    baseline.current = incoming
    setDraft(current => mergeProjectSettings(previous, incoming, current))
  }, [project])
  useEffect(() => { onDirty(project.id, dirty) }, [project.id, dirty, onDirty])
  useEffect(() => () => onDirty(project.id, false), [project.id, onDirty])

  function modelSelect(key: 'worker_profile' | 'strong_profile', label: string) {
    const missing = draft[key] && !profiles.some(p => p.id === draft[key])
    return <Field label={label} hint={`实际使用：${draft[key] || active || '实例默认尚未配置'}`}>
      <Select value={draft[key] || INHERIT} onValueChange={value => setDraft(current => ({ ...current, [key]: value === INHERIT ? '' : value }))} disabled={busy}>
        <SelectTrigger className="w-full min-w-0 text-[12px]" aria-label={`${project.name} 的${label}`}><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem value={INHERIT}>跟随实例默认{active ? ` · ${active}` : ''}</SelectItem>
          {missing && <SelectItem value={draft[key]} disabled>{draft[key]}（配置已缺失）</SelectItem>}
          {profiles.map(p => <SelectItem key={p.id} value={p.id} disabled={!p.runnable || (p.probed && !p.available)}>
            {p.name || p.id} · {p.model || '未填模型'}{!p.runnable ? '（不完整）' : p.probed && !p.available ? '（检测失败）' : ''}
          </SelectItem>)}
        </SelectContent>
      </Select>
    </Field>
  }

  async function save() {
    if (lock.current) return
    lock.current = true
    setBusy(true); setError(''); setNote('')
    try {
      const patch = projectSettingsPatch(baseline.current, draft)
      if (!Object.keys(patch).length) return
      const saved = await api.updateProject(project.id, patch)
      baseline.current = projectSettingsDraft(saved)
      setDraft(baseline.current)
      setNote('已保存。简报与约束用于之后的派发和决策；模型与并发用于后续派发，单轮时限用于下一轮决策，复核策略用于后续结算，前提政策只影响之后的创建/派发。')
      onSaved()
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { lock.current = false; setBusy(false) }
  }

  const worker = draft.worker_profile || active
  const commander = draft.strong_profile || active
  return <Panel
    title={<span className="break-all">{project.name}</span>}
    extra={
      <div className="flex flex-wrap items-center justify-end gap-1.5">
        <Badge variant="muted" size="sm">执行中 {project.running}</Badge>
        {dirty && <Badge variant="warning" size="sm">未保存</Badge>}
      </div>
    }
    bodyClassName="space-y-4"
  >
    <div className="break-all font-mono text-[11px] leading-4 text-ink-3">{project.id}</div>
    <fieldset disabled={busy} className="min-w-0 space-y-4">
      <Field
        label="任务简报 / 授权边界"
        hint="原样进入派发简报与决策：目标与起点、已知资产、时间窗口、禁止动作。新增资产仍以下发消息为准，不需要回来改这里。"
      >
        <Textarea
          aria-label={`${project.name} 的任务简报`}
          rows={4}
          value={draft.scope}
          onChange={e => setDraft(current => ({ ...current, scope: e.target.value }))}
          placeholder="例如：评估 example.com 的外网门户，起点是首页与登录口；只在工作时间测试。"
        />
      </Field>
      <Field
        label="约束文档（可清空取消）"
        hint="项目的测试范围、时间与目标数据保护约定，派发与决策原样读取。清空只取消这份项目约定，角色的证据与交接要求仍然生效。"
      >
        <Textarea
          aria-label={`${project.name} 的约束文档`}
          rows={6}
          className="font-mono text-[12px]"
          value={draft.description}
          onChange={e => setDraft(current => ({ ...current, description: e.target.value }))}
          placeholder="留空 = 本项目不附带约束文档"
        />
      </Field>
      <div className="flex flex-wrap items-center gap-2 rounded-lg border border-hairline bg-surface-2/40 px-3 py-2 text-[11px] leading-4 text-ink-3">
        <Button
          type="button"
          size="xs"
          variant="ghost"
          disabled={busy}
          onClick={() => setDraft(current => ({ ...current, description: DEFAULT_PROJECT_CONSTRAINTS }))}
        >
          填入内置默认
        </Button>
        <Button
          type="button"
          size="xs"
          variant="ghost"
          disabled={busy || !draft.description}
          onClick={() => setDraft(current => ({ ...current, description: '' }))}
        >
          清空（取消约束文档）
        </Button>
        <span className="tw-nums ml-auto">{draft.description.trim().length} 字符</span>
      </div>
      <div className="grid min-w-0 gap-4 md:grid-cols-2">
        {modelSelect('strong_profile', '指挥官模型')}
        {modelSelect('worker_profile', '执行者默认模型')}
      </div>
      <div className="flex flex-wrap items-center gap-2 rounded-lg border border-hairline bg-surface-2/40 px-3 py-2 text-[11px] leading-4 text-ink-3">
        <span>{worker && commander && worker === commander ? '当前共用同一模型配置' : '按任务选择配置，职责与模型档位独立'}</span>
        <Button type="button" size="xs" variant="outline" disabled={busy || !commander || !profiles.some(p => p.id === commander && p.runnable && !(p.probed && !p.available))}
          onClick={() => setDraft(current => ({ ...current, worker_profile: current.strong_profile }))}>执行者与指挥官相同</Button>
      </div>
      <div className="grid min-w-0 gap-4 sm:grid-cols-2">
        <Field label="执行者并发" hint={`留空跟随实例（${defaults.max_runs || '不限'}）；0 表示不限`}>
          <Input aria-label={`${project.name} 的执行者并发`} type="number" min={0} step={1} value={draft.max_runs} placeholder="跟随实例"
            onChange={e => setDraft(current => ({ ...current, max_runs: e.target.value }))} />
        </Field>
        <Field label="指挥官单轮时限" hint={`留空跟随实例（${durationLabel(defaults.round_timeout_seconds)}）；0 表示不限`}>
          <Input aria-label={`${project.name} 的指挥官单轮时限`} value={draft.round_timeout} placeholder="例如 15m / 90s"
            onChange={e => setDraft(current => ({ ...current, round_timeout: e.target.value }))} />
        </Field>
      </div>
      <Callout tone="info" className="leading-relaxed">
        <div className="font-semibold">固定复核（verified_only）</div>
        <div className="mt-1">
          每个意图结束后，新的 vuln/cred 结论先由独立复核者重跑；通过后才 done，未通过进入 review_failed。host/service 等只在下游把事实当必要前提时按需复核。没有自动复核开关，也没有 standard 模式。
        </div>
      </Callout>
      <div className="flex flex-wrap items-center gap-2 border-t border-hairline pt-4">
        <Button size="sm" variant="soft" onClick={() => void save()} disabled={busy || !dirty} aria-busy={busy}>
          {busy && <Spinner size="sm" className="h-3.5 w-3.5" />}
          {busy ? '保存中…' : '保存项目设置'}
        </Button>
        <Button size="sm" variant="ghost" disabled={busy || !dirty} onClick={() => { setDraft(baseline.current); setError(''); setNote('') }}>撤销改动</Button>
      </div>
    </fieldset>
    <div aria-live="polite" className="space-y-2">
      {note && <Callout tone="success">{note}</Callout>}
      {error && <Callout tone="destructive">保存失败：{error}</Callout>}
    </div>
  </Panel>
}
