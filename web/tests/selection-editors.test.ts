import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createServer } from 'vite'
import type { Intent } from '../src/lib/types.ts'

// Load the real TSX and aliases with the application's own transform pipeline.
// The renderer exercises state/effects without a browser or a running project.
let server: Awaited<ReturnType<typeof createServer>>
let ContextEditor: React.ComponentType<any>, NodePanel: React.ComponentType<any>
let api: any
let renderer: ReactTestRenderer | undefined
let sent: { id: string; body: any }[]
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
const originalDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')
const originalFetch = globalThis.fetch

before(async () => {
  server = await createServer({root: fileURLToPath(new URL('..', import.meta.url)), server: {middlewareMode: true}, appType: 'custom'})
  ContextEditor = (await server.ssrLoadModule('/src/components/IntentContextEditor.tsx')).IntentContextEditor
  NodePanel = (await server.ssrLoadModule('/src/components/graph/GraphNodePanel.tsx')).GraphNodePanel
  api = (await server.ssrLoadModule('/src/lib/api.ts')).api
  api.facts = async () => []
  api.children = async () => []
  api.intentUsage = async () => null
  api.intentReadiness = async () => null
  api.profiles = async () => ({profiles: []})
  api.premisePolicy = async () => ({mode: 'recorded'})
  Object.defineProperty(globalThis, 'window', {configurable: true, value: {
    location: {pathname: '/p/fixture/', search: '', hash: ''},
    setInterval, clearInterval,
  }})
  Object.defineProperty(globalThis, 'document', {configurable: true, value: {
    visibilityState: 'visible', addEventListener() {}, removeEventListener() {},
  }})
  globalThis.fetch = async (url, init) => {
    const id = String(url).match(/\/intents\/([^/]+)\/context/)?.[1]
    assert.ok(id, `unexpected request: ${url}`)
    const body = JSON.parse(String(init?.body))
    sent.push({id, body})
    return new Response(JSON.stringify({...intent(id), context_version: body.version + 1,
      context_fact_ids: body.references, pinned_fact_ids: body.pins, interest_entity_refs: body.entities}))
  }
})

afterEach(async () => {
  await act(async () => { renderer?.unmount(); renderer = undefined })
})
after(async () => {
  globalThis.fetch = originalFetch
  if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
  else Reflect.deleteProperty(globalThis, 'window')
  if (originalDocument) Object.defineProperty(globalThis, 'document', originalDocument)
  else Reflect.deleteProperty(globalThis, 'document')
  await server?.close()
})

function intent(id: string, version = 0): Intent {
  return {id, title: id, description: '', status: 'pending', attempts: 0, context_version: version,
    context_fact_ids: [`f_${id}`], interest_entity_refs: [`host:${id}`], created_at: '', updated_at: ''}
}
function button(label: string) {
  return renderer!.root.findAllByType('button').find(node => node.children.join('') === label)!
}
async function openEditor() {
  await act(async () => { button('编辑').props.onClick() })
}
async function editEntities(value: string) {
  await act(async () => { renderer!.root.findByProps({'aria-label': '关注实体'}).props.onChange({target: {value}}) })
}

test('switching intent discards the previous editor draft before a save can target the new intent', async () => {
  sent = []
  const props = {onSaved() {}}
  await act(async () => { renderer = create(React.createElement(ContextEditor, {...props, intent: intent('a')})) })
  await openEditor()
  await editEntities('host:unsaved-a')
  await act(async () => { renderer!.update(React.createElement(ContextEditor, {...props, intent: intent('b')})) })
  await editEntities('host:edited-b')
  await act(async () => { button('保存上下文').props.onClick() })
  assert.deepEqual(sent, [{id: 'b', body: {version: 0, references: ['f_b'], pins: [], entities: ['host:edited-b']}}])
})

test('a newer context version with unchanged contents advances the save token and preserves the draft', async () => {
  sent = []
  const props = {onSaved() {}}
  await act(async () => { renderer = create(React.createElement(ContextEditor, {...props, intent: intent('a')})) })
  await openEditor()
  await editEntities('host:my-draft')
  await act(async () => { renderer!.update(React.createElement(ContextEditor, {...props, intent: intent('a', 1)})) })
  await act(async () => { button('保存上下文').props.onClick() })
  assert.equal(sent[0].body.version, 1)
  assert.deepEqual(sent[0].body.entities, ['host:my-draft'])
})

test('switching graph nodes retires the previous fact detail while the next request is pending', async () => {
  const fact = (id: string) => ({id, title: id, kind: 'note', status: 'unverified', confidence: 'observed', created_at: ''})
  api.fact = async (id: string) => id === 'a'
    ? {fact: fact(id), evidence: [{id: 'e_a', name: 'proof-for-a', uri: 'a.txt'}], reviews: []}
    : new Promise(() => {})
  const props = {facts: [], runs: [], onSelectFact() {}, onClose() {}, onChanged() {}, childRefresh: 0, onChildIntent() {}}
  const node = (id: string) => ({id: `f:${id}`, type: 'fact', label: id, meta: {fact: fact(id)}})
  await act(async () => { renderer = create(React.createElement(NodePanel, {...props, node: node('a')})) })
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('proof-for-a'))
  await act(async () => { renderer!.update(React.createElement(NodePanel, {...props, node: node('b')})) })
  assert.ok(!JSON.stringify(renderer!.toJSON()).includes('proof-for-a'), 'fact A evidence was displayed under fact B')
})
