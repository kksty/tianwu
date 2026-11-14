import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ApiError } from '../src/lib/api.ts'
import {
  activeInstructions,
  createInstructionFingerprint,
  createInstructionRequest,
  instructionErrorMessage,
  instructionNoticeChanges,
  instructionPreview,
  instructionScopeLabel,
  instructionSummary,
  isInstructionNotice,
  newestInstructionNotice,
  sortInstructions,
} from '../src/lib/instructions.ts'
import type { Instruction } from '../src/lib/types.ts'

function row(over: Partial<Instruction> = {}): Instruction {
  return {
    id: 'instruction_1',
    text: '先不拿 shell',
    version: 1,
    state: 'active',
    created_by: 'operator',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

test('scope label separates the project-wide set from a direction subtree', () => {
  assert.equal(instructionScopeLabel(row()), '整个项目')
  assert.equal(instructionScopeLabel(row({ intent_id: 'in_1' })), '方向 in_1 及后代')
})

test('the list reads active first and newest-updated first, so a corrected row is never below its replacement', () => {
  const sorted = sortInstructions([
    row({ id: 'ins_old', updated_at: '2026-01-01T00:00:00Z' }),
    row({ id: 'ins_revoked', state: 'revoked', updated_at: '2026-03-01T00:00:00Z' }),
    row({ id: 'ins_new', updated_at: '2026-02-01T00:00:00Z' }),
  ])
  assert.deepEqual(sorted.map((r) => r.id), ['ins_new', 'ins_old', 'ins_revoked'])
  assert.equal(activeInstructions(sorted).length, 2)
})

test('the summary counts the standing set and hides revoked rows in one line', () => {
  assert.equal(instructionSummary([]), '当前没有有效持久指令')
  assert.equal(instructionSummary([row()]), '当前生效 1 条')
  assert.equal(instructionSummary([row(), row({ id: 'b', state: 'revoked' })]), '当前生效 1 条，另有 1 条已撤销')
})

test('preview collapses whitespace and bounds the line', () => {
  assert.equal(instructionPreview('  a\n\n  b  '), 'a b')
  assert.equal(instructionPreview('x'.repeat(200), 20), `${'x'.repeat(20)}…`)
})

test('the create body keeps project scope implicit and trims the direction id', () => {
  assert.deepEqual(createInstructionRequest({ text: ' 边界 ', intentID: '' }, 'k1'), {
    text: '边界',
    idempotency_key: 'k1',
  })
  assert.deepEqual(createInstructionRequest({ text: '边界', intentID: ' in_1 ' }, 'k2'), {
    text: '边界',
    intent_id: 'in_1',
    idempotency_key: 'k2',
  })
})

test('the create fingerprint follows content and scope, so a retry cannot duplicate a row', () => {
  const base = createInstructionFingerprint({ text: '边界', intentID: '' })
  assert.equal(base, createInstructionFingerprint({ text: '  边界  ', intentID: '' }))
  assert.notEqual(base, createInstructionFingerprint({ text: '边界二', intentID: '' }))
  assert.notEqual(base, createInstructionFingerprint({ text: '边界', intentID: 'in_1' }))
})

test('a version conflict says how to recover instead of "409"', () => {
  assert.match(instructionErrorMessage(new ApiError(409, 'conflict', '')), /版本冲突/)
  assert.equal(instructionErrorMessage(new ApiError(400, '正文应为 1–4000 字', '')), '正文应为 1–4000 字')
  assert.equal(instructionErrorMessage(new Error('网络错误')), '网络错误')
})

test('instruction-change notices fold out of the conversation, not out of the record', () => {
  const messages = [
    { kind: 'instruction', created_at: '2026-01-01T00:00:00Z' },
    { kind: 'say', created_at: '2026-01-02T00:00:00Z' },
    { kind: 'instruction', created_at: '2026-01-03T00:00:00Z' },
  ]
  assert.deepEqual(messages.filter(isInstructionNotice).map((m) => m.created_at), [
    '2026-01-01T00:00:00Z',
    '2026-01-03T00:00:00Z',
  ])
  assert.equal(newestInstructionNotice(messages)?.created_at, '2026-01-03T00:00:00Z')
  assert.equal(newestInstructionNotice([{ kind: 'say', created_at: '2026-01-02T00:00:00Z' }]), undefined)
  assert.equal(newestInstructionNotice([]), undefined)
})

test('the folded count is revisions, not deliveries: one change fans out', () => {
  const same = [
    { kind: 'instruction', instruction_id: 'ins_1', instruction_version: 2 },
    { kind: 'instruction', instruction_id: 'ins_1', instruction_version: 2 },
    { kind: 'say' },
  ]
  assert.equal(instructionNoticeChanges(same), 1)
  // A new revision of the same row is a second change.
  assert.equal(instructionNoticeChanges([...same, { kind: 'instruction', instruction_id: 'ins_1', instruction_version: 3 }]), 2)
  // A notice without an id still counts once rather than disappearing.
  assert.equal(instructionNoticeChanges([{ kind: 'instruction' }]), 1)
})
