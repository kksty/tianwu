import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createServer } from 'vite'

let server: Awaited<ReturnType<typeof createServer>>
let renderer: ReactTestRenderer | undefined
let useSessionState: any, usePoll: any, Reports: any, SettingsPage: any, ConfirmProvider: any, api: any
const storage = new Map<string, string>()
const timers = new Map<number, () => void>()
let timerID = 0
const oldWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
const oldDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')

before(async () => {
  server = await createServer({root: fileURLToPath(new URL('..', import.meta.url)), server: {middlewareMode: true, hmr: false}, appType: 'custom'})
  useSessionState = (await server.ssrLoadModule('/src/lib/session-state.ts')).useSessionState
  usePoll = (await server.ssrLoadModule('/src/lib/hooks.ts')).usePoll
  Reports = (await server.ssrLoadModule('/src/views/Reports.tsx')).Reports
  SettingsPage = (await server.ssrLoadModule('/src/views/Settings.tsx')).SettingsPage
  ConfirmProvider = (await server.ssrLoadModule('@cyber/ui')).ConfirmProvider
  api = (await server.ssrLoadModule('/src/lib/api.ts')).api
  Object.defineProperty(globalThis, 'window', {configurable: true, value: {
    location: {pathname: '/p/alpha/', search: '', hash: ''},
    sessionStorage: {getItem: (key: string) => storage.get(key) ?? null, setItem: (key: string, value: string) => storage.set(key, value)},
    setInterval: (fn: () => void) => { timers.set(++timerID, fn); return timerID },
    clearInterval: (id: number) => timers.delete(id),
    addEventListener() {}, removeEventListener() {},
  }})
  Object.defineProperty(globalThis, 'document', {configurable: true, value: {
    visibilityState: 'visible', addEventListener() {}, removeEventListener() {},
  }})
})
afterEach(async () => {
  await act(async () => { renderer?.unmount(); renderer = undefined })
  storage.clear()
  timers.clear()
  window.location.pathname = '/p/alpha/'
})
after(async () => {
  if (oldWindow) Object.defineProperty(globalThis, 'window', oldWindow)
  else Reflect.deleteProperty(globalThis, 'window')
  if (oldDocument) Object.defineProperty(globalThis, 'document', oldDocument)
  else Reflect.deleteProperty(globalThis, 'document')
  await server.close()
})

test('draft edits survive immediate navigation, stay project-scoped and clear on successful submission', async () => {
  let state: any
  function Draft() { state = useSessionState('message', ''); return null }
  await act(async () => { renderer = create(React.createElement(Draft)) })
  await act(async () => { state[1]('unfinished'); renderer!.unmount() })
  window.location.pathname = '/p/beta/'
  await act(async () => { renderer = create(React.createElement(Draft)) })
  assert.equal(state[0], '')
  await act(async () => { renderer!.unmount() })
  window.location.pathname = '/p/alpha/'
  await act(async () => { renderer = create(React.createElement(Draft)) })
  assert.equal(state[0], 'unfinished')
  await act(async () => { state[1](''); renderer!.unmount() })
  await act(async () => { renderer = create(React.createElement(Draft)) })
  assert.equal(state[0], '')
})

test('damaged persisted filters fall back to defaults instead of breaking the graph', async () => {
  storage.set('tw.viewMemory.v1', JSON.stringify({'draft:/p/alpha/': {filters: null}}))
  let value: any
  function Filters() { [value] = useSessionState('filters', {hiddenKinds: [], hideDone: false}); return null }
  await act(async () => { renderer = create(React.createElement(Filters)) })
  assert.deepEqual(value, {hiddenKinds: [], hideDone: false})
})

test('failed refresh preserves data and its timestamp, then a retry clears the error', async () => {
  let fail = false, state: any
  const load = async () => { if (fail) throw new Error('offline'); return ['run-1'] }
  function Monitor() { state = usePoll(load, 4000); return null }
  await act(async () => { renderer = create(React.createElement(Monitor)) })
  const timestamp = state.updatedAt
  fail = true
  await act(async () => { state.reload() })
  assert.deepEqual(state.data, ['run-1'])
  assert.equal(state.updatedAt, timestamp)
  assert.equal(state.error, 'offline')
  fail = false
  await act(async () => { state.reload() })
  assert.equal(state.error, undefined)
})

test('report polling continues past the first refresh and finds a completed report despite server clock skew', async () => {
  let rows: any[] = [], runs: any[] = [], running = false, posts = 0
  api.reports = async () => rows
  api.runs = async () => runs
  api.reportPrecheck = async () => ({running, unverified: 0})
  api.createReport = async () => { posts++; return {queued: true} }
  await act(async () => { renderer = create(React.createElement(Reports)) })
  const generate = () => renderer!.root.findAllByType('button').find(node => node.children.includes('生成报告'))!
  await act(async () => { await generate().props.onClick() })
  assert.equal(posts, 1)
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('已排队'))
  for (let i = 0; i < 3; i++) await act(async () => { for (const tick of timers.values()) tick() })
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('已排队'))
  running = true
  runs = [{id: 'new-run', purpose: 'report', status: 'running', started_at: '2020-01-01T00:00:00Z'}]
  await act(async () => { for (const tick of timers.values()) tick() })
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('生成中'))
  running = false
  runs = [{...runs[0], status: 'done'}]
  rows = [{id: 'new-report', status: 'ready', findings: 2, created_at: '2020-01-01T00:00:00Z'}]
  await act(async () => { for (const tick of timers.values()) tick() })
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('new-report'))
  assert.equal(generate().props.disabled, false)
})

test('model controls pause and resume without activation, and connectivity runs only on explicit click', async () => {
  let profile: any = {id: 'one', model: 'configured', runnable: true, api_key_set: true, revision: 'same', active: true}
  const pauses: boolean[] = []
  let checks = 0
  api.settings = async () => ({active_profile: 'one'})
  api.projectIndex = async () => ({projects: []})
  api.profiles = async () => ({profiles: [{...profile}], active_profile: 'one'})
  api.activateProfile = async () => { throw new Error('toggle must not activate or restart a host') }
  api.pauseProfile = async (_id: string, paused: boolean) => { pauses.push(paused); profile = {...profile, paused, runnable: !paused}; return profile }
  api.probeProfile = async () => { checks++; profile = {...profile, probed: true, available: true, probe_kind: 'connectivity', returned_model: 'actual-model'}; return profile }
  await act(async () => { renderer = create(React.createElement(ConfirmProvider, null, React.createElement(SettingsPage))) })
  assert.equal(checks, 0)
  const pause = () => renderer!.root.findByProps({'aria-label': '暂停使用 one'})
  await act(async () => { pause().props.onClick() })
  assert.deepEqual(pauses, [true])
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('已暂停'))
  const check = () => renderer!.root.findAllByType('button').find(node => node.children.includes('连通性检测'))!
  assert.equal(check().props.disabled, true)
  await act(async () => { renderer!.root.findByProps({'aria-label': '恢复使用 one'}).props.onClick() })
  assert.deepEqual(pauses, [true, false])
  await act(async () => { check().props.onClick() })
  assert.equal(checks, 1)
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('actual-model'))
})
