import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  DEFAULT_PROJECT_CONSTRAINTS,
  projectDescriptionDraft,
} from '../src/lib/project-defaults.ts'

test('project constraints cover authorization and target data, independently of role evidence contracts', () => {
  assert.equal(DEFAULT_PROJECT_CONSTRAINTS.trim(), DEFAULT_PROJECT_CONSTRAINTS)
  for (const phrase of ['明确授权', '测试时间', '默认只读', '真实业务数据', '影响可用性']) {
    assert.match(DEFAULT_PROJECT_CONSTRAINTS, new RegExp(phrase))
  }
  assert.doesNotMatch(DEFAULT_PROJECT_CONSTRAINTS, /原始证据|已验证结论|inferred|人工请求/)
  // The built-in document is exactly three items: the operator removed the
  // former fourth clause (stop on out-of-scope / unclear-ownership assets and
  // ask first) on 2026-11-13. The removal is deliberate, so pin it here — a
  // later "restore from git history" would otherwise look like a fix and ship
  // text the operator did not choose.
  assert.equal(DEFAULT_PROJECT_CONSTRAINTS.split('\n').length, 3)
  assert.doesNotMatch(DEFAULT_PROJECT_CONSTRAINTS, /归属不明|可能影响他人|由操作员确认后再继续|^4\./m)
})

// The operator does not want the built-in document to read as "everything is
// read-only": upload-interface testing and test-data cleanup need writes. The
// document must therefore allow authorised, scoped writes/uploads explicitly
// (default stays read-only), while keeping the hard limits separate — most
// importantly, "never delete or modify real data" is absolute and cannot be
// relaxed by any authorisation. Both halves are pinned so a copy edit cannot
// silently swing it either way.
test('built-in constraints allow authorised scoped writes instead of a blanket read-only rule', () => {
  // Authorised write/delete/upload is stated as allowed, scoped to test data.
  assert.match(DEFAULT_PROJECT_CONSTRAINTS, /默认只读/)
  assert.match(DEFAULT_PROJECT_CONSTRAINTS, /受控的写入、删改与上传/)
  assert.match(DEFAULT_PROJECT_CONSTRAINTS, /上传/)
  assert.match(DEFAULT_PROJECT_CONSTRAINTS, /只作用于授权资产和专用测试数据/)
  assert.match(DEFAULT_PROJECT_CONSTRAINTS, /不触碰真实数据/)
  assert.match(DEFAULT_PROJECT_CONSTRAINTS, /结束后按需清理或恢复/)
  // The old blanket clause (no writes at all) must not come back.
  assert.doesNotMatch(DEFAULT_PROJECT_CONSTRAINTS, /仅做只读验证|不新增、修改或删除目标系统/)
  // "Never delete or modify real data" survives as the one absolute rule: it is
  // stated as not relaxable, and even an explicit authorisation cannot change
  // real data. This is the operator's hard requirement on the document.
  assert.match(DEFAULT_PROJECT_CONSTRAINTS, /不删改真实数据（含真实业务数据、账号、权限、配置和文件）/)
  assert.match(DEFAULT_PROJECT_CONSTRAINTS, /这条不可放宽/)
  assert.match(DEFAULT_PROJECT_CONSTRAINTS, /即使有授权也不做真实数据的删除或修改/)
  // Only the other side effects are separately authorisable, not real data.
  assert.match(DEFAULT_PROJECT_CONSTRAINTS, /影响可用性、不可逆或触发其他真实业务副作用/)
  assert.match(DEFAULT_PROJECT_CONSTRAINTS, /等待单独授权/)
})

test('a cancelled constraint document sends no description, never stale text', () => {
  assert.equal(projectDescriptionDraft(false, DEFAULT_PROJECT_CONSTRAINTS), '')
  assert.equal(projectDescriptionDraft(true, `  ${DEFAULT_PROJECT_CONSTRAINTS}  `), DEFAULT_PROJECT_CONSTRAINTS)
  assert.equal(projectDescriptionDraft(true, '   '), '')
  assert.equal(projectDescriptionDraft(true, ''), '')
})
