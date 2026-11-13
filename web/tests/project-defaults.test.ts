import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  DEFAULT_PROJECT_CONSTRAINTS,
  projectDescriptionDraft,
  projectNameFromBrief,
} from '../src/lib/project-defaults.ts'

test('project constraints cover authorization and target data, independently of role evidence contracts', () => {
  assert.equal(DEFAULT_PROJECT_CONSTRAINTS.trim(), DEFAULT_PROJECT_CONSTRAINTS)
  for (const phrase of ['明确授权', '测试时间', '只读验证', '不新增、修改或删除目标系统', '业务数据', '影响可用性', '范围外']) {
    assert.match(DEFAULT_PROJECT_CONSTRAINTS, new RegExp(phrase))
  }
  assert.doesNotMatch(DEFAULT_PROJECT_CONSTRAINTS, /原始证据|已验证结论|inferred|人工请求/)
})

test('the derived name preview mirrors the server rule: first non-empty line, trimmed', () => {
  assert.equal(projectNameFromBrief('第一行简报\n第二行'), '第一行简报')
  assert.equal(projectNameFromBrief('\n\n   第一行简报   \n第二行'), '第一行简报')
  // The whole first line, not the first sentence: the server does not split it.
  assert.equal(projectNameFromBrief('目标是 example.com。已知登录口'), '目标是 example.com。已知登录口')
  assert.equal(projectNameFromBrief(''), '未命名项目')
  assert.equal(projectNameFromBrief('   \n\t  '), '未命名项目')
})

test('the name preview clips at 40 runes with an ellipsis, counting characters not bytes', () => {
  const forty = '门'.repeat(40)
  assert.equal(projectNameFromBrief(forty), forty)
  assert.equal(projectNameFromBrief(`${forty}户`), `${forty}…`)
  // Surrogate pairs count as one rune each (emoji must not be split).
  const emoji = '😀'.repeat(41)
  const preview = projectNameFromBrief(emoji)
  assert.equal(Array.from(preview).length, 41, '40 emoji + the ellipsis')
  assert.ok(preview.endsWith('…'))
})

test('a cancelled constraint document sends no description, never stale text', () => {
  assert.equal(projectDescriptionDraft(false, DEFAULT_PROJECT_CONSTRAINTS), '')
  assert.equal(projectDescriptionDraft(true, `  ${DEFAULT_PROJECT_CONSTRAINTS}  `), DEFAULT_PROJECT_CONSTRAINTS)
  assert.equal(projectDescriptionDraft(true, '   '), '')
  assert.equal(projectDescriptionDraft(true, ''), '')
})
