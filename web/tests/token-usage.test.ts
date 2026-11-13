import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createServer } from 'vite'

// Regression for the header token read-out: the trigger used to carry a native
// `title` *and* a hover panel, so after a second of hovering the browser's grey
// tooltip painted over the open panel. The rule is now "one surface": no native
// title, and every caveat lives inside the hover/focus panel.

let server: Awaited<ReturnType<typeof createServer>>
let ProjectTokens: React.ComponentType<any>
let renderer: ReactTestRenderer | undefined

before(async () => {
  server = await createServer({
    root: fileURLToPath(new URL('..', import.meta.url)),
    server: { middlewareMode: true, hmr: false },
    appType: 'custom',
  })
  ProjectTokens = (await server.ssrLoadModule('/src/components/TokenUsage.tsx')).ProjectTokens
})

afterEach(async () => {
  await act(async () => { renderer?.unmount(); renderer = undefined })
})

after(async () => {
  await server.close()
})

async function render(usage: any) {
  await act(async () => {
    if (renderer) renderer.unmount()
    renderer = create(React.createElement(ProjectTokens, { usage }))
  })
  return renderer!
}

const text = (r: ReactTestRenderer) => JSON.stringify(r.toJSON())

test('token read-out has no native title: one hover panel is the only surface', async () => {
  const r = await render({ total_tokens: 1453311, input_tokens: 1426151, output_tokens: 27160, cached_input_tokens: 1332992, requests: 42 })
  const trigger = r.root.findByProps({ 'data-testid': 'project-tokens' })
  assert.equal(trigger.props.title, undefined, 'the trigger must not carry a native title')
  // Exactly one absolute panel: the breakdown.
  const panels = r.root.findAll((n) => typeof n.type === 'string' && n.props.className?.includes('absolute'))
  assert.equal(panels.length, 1, 'expected exactly one hover panel')
})

test('the hover panel carries the full breakdown and the usage caveats', async () => {
  const r = await render({ total_tokens: 100, input_tokens: 90, output_tokens: 10, cached_input_tokens: 40, requests: 3, missing_usage: 2, unknown_usage_runs: 1 })
  const body = text(r)
  for (const part of ['入', '出', '缓存', '请求', '只记录模型上报用量', '次请求未上报用量', '个 run 的用量未知']) {
    assert.ok(body.includes(part), `panel is missing "${part}": ${body}`)
  }
})

test('unknown usage says so instead of fabricating a zero', async () => {
  const r = await render(undefined)
  const body = text(r)
  assert.ok(body.includes('—'), 'unknown total renders as an em dash')
  assert.ok(body.includes('模型没有上报用量'), 'the panel explains why the number is absent')
  const known = await render({ total_tokens: 0, input_tokens: 0, output_tokens: 0, requests: 0, known: false })
  assert.ok(text(known).includes('模型没有上报用量'), 'known:false is treated as unknown even when the numbers are zero')
})
