import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mergeProjectSettings, nonNegativeInteger, profileStatus, projectSettingsDraft, projectSettingsPatch } from '../src/lib/settings.ts'
import type { ProjectInfo } from '../src/lib/types.ts'

const project = { id: 'p', worker_profile: '', strong_profile: '', max_runs: null, round_timeout: '', verify_auto: null } as ProjectInfo

test('refresh preserves unsaved fields while updating untouched settings', () => {
  const previous = projectSettingsDraft(project)
  const incoming = projectSettingsDraft({ ...project, worker_profile: 'new-default', max_runs: 8 })
  const draft = { ...previous, max_runs: '3', strong_profile: 'same-model' }
  assert.deepEqual(mergeProjectSettings(previous, incoming, draft), { ...draft, worker_profile: 'new-default' })
})

test('project save distinguishes inheritance from zero and only updates changed fields', () => {
  const previous = projectSettingsDraft({ ...project, max_runs: 4, verify_auto: true, worker_profile: 'one' })
  assert.deepEqual(projectSettingsPatch(previous, { ...previous, max_runs: '', verify_auto: '' }), { max_runs: null, verify_auto: null })
  assert.deepEqual(projectSettingsPatch(previous, { ...previous, max_runs: '0', verify_auto: 'off' }), { max_runs: 0, verify_auto: false })
  assert.deepEqual(projectSettingsPatch(previous, { ...previous, strong_profile: 'one' }), { strong_profile: 'one' })
})

test('invalid numeric input cannot silently turn into an unlimited value', () => {
  for (const raw of ['-1', '1.5', '1e3', 'NaN', '9007199254740992']) assert.throws(() => nonNegativeInteger(raw, '并发'))
  assert.equal(nonNegativeInteger('', '并发'), 0)
  assert.equal(nonNegativeInteger(' 12 ', '并发'), 12)
})

test('model directory detection distinguishes unknown from failure and inference validation', () => {
  assert.equal(profileStatus({ runnable: true, probed: false, probe_error: '无目录' }).label, '待确认')
  assert.equal(profileStatus({ runnable: true, probed: true, available: false }).tone, 'danger')
  assert.match(profileStatus({ runnable: true, probed: true, available: true }).detail, /不代表实际推理/)
  assert.equal(profileStatus({ runnable: false, probed: true, available: true }).label, '配置不完整')
})

test('paused model profiles stay visibly paused regardless of a successful probe', () => {
  const status = profileStatus({ runnable: false, paused: true, probed: true, available: true, probe_kind: 'connectivity' })
  assert.equal(status.label, '已暂停')
  assert.match(status.detail, /不接受新运行/)
})

test('connectivity shows the returned model without substituting the requested model', () => {
  const probe = { runnable: true, probed: true, available: true, probe_kind: 'connectivity' as const }
  assert.match(profileStatus({ ...probe, returned_model: 'served-alias' }).detail, /served-alias/)
  assert.match(profileStatus(probe).detail, /响应模型：未返回/)
  assert.equal(profileStatus({ ...probe, available: false }).label, '连接失败')
})
