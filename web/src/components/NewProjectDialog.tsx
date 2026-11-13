// Simplified bootstrap: a display name + one paragraph + the built-in
// constraint document.
//
// The name is only a display name: the directory id is a short random id
// (p-xxxxxxxx) decoupled from it, so renaming a project never moves its
// directory. A paragraph saying where we start and what we want becomes the
// project brief (scope), and the standing constraints are a prefilled document
// the operator may edit or cancel (it becomes the project description). Starting
// a round is the default and does not require a goal: the commander writes goals
// into the ledger from the brief.
import { useEffect, useState } from 'react'
import {
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Field,
  Input,
  Switch,
  Textarea,
} from '@cyber/ui'
import { api } from '@/lib/api'
import { DEFAULT_PROJECT_CONSTRAINTS, projectDescriptionDraft } from '@/lib/project-defaults'

export function NewProjectDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated?: () => void
}) {
  // The display name: required, and the only thing a rename would change.
  const [projectName, setProjectName] = useState('')
  // The one required input: where we start and what we want.
  const [brief, setBrief] = useState('')
  // The built-in constraint document is on by default, editable, and can be
  // cancelled (off = no description); turning it back on restores the default.
  const [constraintsOn, setConstraintsOn] = useState(true)
  const [constraints, setConstraints] = useState(DEFAULT_PROJECT_CONSTRAINTS)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // The name is only flagged after a submit attempt: opening an empty form must
  // not look like the operator already made a mistake.
  const [attempted, setAttempted] = useState(false)

  useEffect(() => {
    if (open) setAttempted(false)
  }, [open])

  const nameBlank = !projectName.trim()
  const constraintsEdited = constraints !== DEFAULT_PROJECT_CONSTRAINTS

  function toggleConstraints(on: boolean) {
    setConstraintsOn(on)
    setConstraints(on ? DEFAULT_PROJECT_CONSTRAINTS : '')
  }

  async function submit(start: boolean) {
    const name = projectName.trim()
    const paragraph = brief.trim()
    setAttempted(true)
    if (!name) {
      setError('项目名称必填：这是控制台与项目列表里的显示名，不能用空白。')
      return
    }
    if (!paragraph) {
      setError('先写一段目标与起点：这段话会成为项目简报。')
      return
    }
    setBusy(true)
    setError('')
    try {
      const created = await api.createProject(
        name,
        paragraph,
        '',
        '',
        projectDescriptionDraft(constraintsOn, constraints),
      )
      setProjectName('')
      setBrief('')
      setConstraintsOn(true)
      setConstraints(DEFAULT_PROJECT_CONSTRAINTS)
      onOpenChange(false)
      onCreated?.()
      window.location.assign(
        `/p/${encodeURIComponent(created.id)}/?view=overview${start ? '&autorun=1' : ''}`,
      )
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>新建项目</DialogTitle>
          <DialogDescription>
            项目名称 + 一段自然语言简报；起点就是这段简报，终点由指挥官拆成唯一目标。名称只是显示名：目录使用稳定的短 ID（p-xxxxxxxx），之后改名不会改路径。内置约束文档随项目附带，可以编辑或取消。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <Field
            label="项目名称"
            hint="控制台与项目列表里的显示名，必填；目录使用与名称无关的稳定短 ID，改名不会改路径。"
            error={attempted && nameBlank ? '项目名称不能为空：请填写一个显示名（不能用空白）。' : undefined}
          >
            <Input
              value={projectName}
              onChange={(e) => setProjectName(e.target.value)}
              autoFocus
              aria-label="项目名称"
              placeholder="例如：NX 门户外网评估"
            />
          </Field>

          <Field
            label="用一段话说明目标与起点"
            hint="目标 URL/域名、已知资产、背景、已知的结束条件；这段话会作为项目简报原样交给指挥官与执行者。"
          >
            <Textarea
              rows={6}
              value={brief}
              onChange={(e) => setBrief(e.target.value)}
              placeholder={
                '例如：我们的目标是评估 example.com 的外网门户，起点是首页和登录口，已知一台测试账号；\n' +
                '希望确认是否存在未授权访问，并留下可复核的原始响应。\n' +
                '只在工作时间测试，不碰与门户无关的资产。'
              }
            />
          </Field>

          <div className="rounded-md border border-border bg-muted/30">
            <div className="flex items-start gap-3 px-3 py-2">
              <Switch
                checked={constraintsOn}
                onCheckedChange={toggleConstraints}
                id="builtin-constraints"
              />
              <label
                htmlFor="builtin-constraints"
                className="min-w-0 flex-1 cursor-pointer text-[12px] leading-relaxed text-muted-foreground"
              >
                <span className="block font-medium text-foreground">内置约束文档（可编辑，可取消）</span>
                约定测试范围、时间和目标数据保护，随项目进入派发与决策。可编辑或关闭；证据与结论的交付要求由角色提示词持续提供。
              </label>
            </div>
            {constraintsOn ? (
              <div className="border-t border-border/60 p-3">
                <Textarea
                  rows={10}
                  aria-label="内置约束文档"
                  className="font-mono text-[12px]"
                  value={constraints}
                  onChange={(e) => setConstraints(e.target.value)}
                />
                <div className="mt-1.5 flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                  <span>
                    {constraintsEdited ? '已修改内置默认文本' : '内置默认文本'} ·{' '}
                    {constraints.trim().length} 字符
                  </span>
                  {constraintsEdited && (
                    <Button
                      type="button"
                      size="xs"
                      variant="ghost"
                      onClick={() => setConstraints(DEFAULT_PROJECT_CONSTRAINTS)}
                    >
                      恢复内置默认
                    </Button>
                  )}
                </div>
              </div>
            ) : (
              <div className="border-t border-border/60 px-3 py-2 text-[11px] text-muted-foreground">
                已取消：本次创建不发送约束文档（项目描述为空）。
              </div>
            )}
          </div>

          {error && <div className="text-[12px] text-destructive">{error}</div>}
          <Callout tone="info">
            「创建并开始」会立刻让指挥官跑一轮（需要已配置模型）；失败会在概览页提示，不会静默重试。只想先建项目就选「只创建」。
          </Callout>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button
            variant="ghost"
            onClick={() => void submit(false)}
            disabled={busy || nameBlank || !brief.trim()}
          >
            只创建
          </Button>
          <Button onClick={() => void submit(true)} disabled={busy || nameBlank || !brief.trim()}>
            {busy ? '创建中…' : '创建并开始'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
