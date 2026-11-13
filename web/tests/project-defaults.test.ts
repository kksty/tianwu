import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  DEFAULT_PROJECT_CONSTRAINTS,
  projectDescriptionDraft,
} from '../src/lib/project-defaults.ts'

test('project constraints cover authorization and target data, independently of role evidence contracts', () => {
  assert.equal(DEFAULT_PROJECT_CONSTRAINTS.trim(), DEFAULT_PROJECT_CONSTRAINTS)
  for (const phrase of ['明确授权', '测试时间', '只读验证', '不新增、修改或删除目标系统', '业务数据', '影响可用性', '范围外']) {
    assert.match(DEFAULT_PROJECT_CONSTRAINTS, new RegExp(phrase))
  }
  assert.doesNotMatch(DEFAULT_PROJECT_CONSTRAINTS, /原始证据|已验证结论|inferred|人工请求/)
})

test('a cancelled constraint document sends no description, never stale text', () => {
  assert.equal(projectDescriptionDraft(false, DEFAULT_PROJECT_CONSTRAINTS), '')
  assert.equal(projectDescriptionDraft(true, `  ${DEFAULT_PROJECT_CONSTRAINTS}  `), DEFAULT_PROJECT_CONSTRAINTS)
  assert.equal(projectDescriptionDraft(true, '   '), '')
  assert.equal(projectDescriptionDraft(true, ''), '')
})
