import { test } from 'node:test'
import assert from 'node:assert/strict'
import { bodyPreview } from '../src/lib/traffic.ts'

test('HTTP bodies preserve UTF-8 text and render binary as hex', () => {
  assert.equal(bodyPreview(Buffer.from('中文<script>文本</script>').toString('base64')).text, '中文<script>文本</script>')
  const binary = bodyPreview(Buffer.from([0, 255, 128]).toString('base64'))
  assert.equal(binary.binary, true)
  assert.equal(binary.text, '00 ff 80')
})

test('a text preview boundary does not misclassify a split UTF-8 character', () => {
  const preview = bodyPreview(Buffer.from('中文测试').toString('base64'), 4)
  assert.equal(preview.text, '中')
  assert.equal(preview.truncated, true)
  assert.equal(preview.binary, false)
  assert.equal(preview.size, 12)
})
