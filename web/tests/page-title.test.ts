// The browser tab is how an operator tells projects apart, so the title rule is
// tested directly: app name first, only the named parts, no dangling separators.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { APP_NAME, pageTitle } from '../src/lib/page-title.ts'

test('pageTitle puts the app first and joins the named parts', () => {
  assert.equal(APP_NAME, '天吴')
  assert.equal(pageTitle(['客户 A', '图谱']), '天吴 · 客户 A · 图谱')
  assert.equal(pageTitle(['态势']), '天吴 · 态势')
})

test('pageTitle skips missing parts instead of leaving separators behind', () => {
  assert.equal(pageTitle([]), APP_NAME)
  assert.equal(pageTitle([undefined, null, '']), APP_NAME)
  assert.equal(pageTitle(['客户 A', undefined, '图谱']), '天吴 · 客户 A · 图谱')
  assert.equal(pageTitle(['  ', '']), APP_NAME)
})

test('pageTitle trims each part so a stray space does not read as a segment', () => {
  assert.equal(pageTitle(['  客户 A  ', ' 图谱 ']), '天吴 · 客户 A · 图谱')
})
