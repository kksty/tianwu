import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  buildRecordMaps,
  describeRecord,
  eventLabel,
  messageKindLabel,
  participantName,
  parsePeerMessage,
  resolveEndpoint,
  shortAddress,
} from '../src/lib/ioa.ts'
import type { IOARecord, Participant } from '../src/lib/types.ts'

// The fixtures below mirror the shapes a portal run produces: the
// commander talks through the control plane (empty envelope source, `[collab]`
// prelude), while intents answer through their own harness session and the
// reply's IOA target is the control plane's node.
const people: Participant[] = [
  { id: 'commander', kind: 'commander', title: '指挥官', contactable: true },
  { id: 'pt_in_a484fc1d', kind: 'intent', intent_id: 'in_a484fc1d', title: '侦察与资产测绘：portal.example', status: 'done', contactable: false },
  { id: 'pt_in_fb5e6d72', kind: 'intent', intent_id: 'in_fb5e6d72', title: '应用层测试：portal.example 主站与接口', status: 'running', contactable: true },
]

const sessions: Record<string, string> = {
  '194adc7aca2b87015f6d4c5c472b242e': '指挥官',
  '9da4e9bb76501b6b364c7345664e6c5a': '侦察与资产测绘：portal.example',
  '9adf25f634fdc8d84221061002ba38e1': '应用层测试：portal.example 主站与接口',
  'b27ae7b3422dfd93c2e4dbe601af5293': '组件漏洞排查：portal.example',
}

const commanderSay: IOARecord = {
  seq: 1,
  ioa_message_id: '575284f6c09bcc91ce10a4d9c292fc26',
  sender: 'cd3ecb7f657f17e27aef96ff92cf9312',
  created_at: '2026-11-11T07:09:47.8056262Z',
  observed_at: '2026-11-11T07:09:49.812748Z',
  envelope: {
    event: 'message',
    id: 'm_20f911f4',
    source: {},
    target: { session_id: '9da4e9bb76501b6b364c7345664e6c5a' },
  },
  content: {
    text: '[collab] from=commander to=pt_in_a484fc1d kind=say message_id=m_20f911f4\n指挥官现场提示（已亲自验证）：目标可达。HTTP GET 返回 200。',
  },
}

const workerReport: IOARecord = {
  seq: 14,
  ioa_message_id: 'a1b2c3',
  sender: '28cd3cd7aa11bb22cc33dd44ee55ff66',
  created_at: '2026-11-11T07:20:00Z',
  observed_at: '2026-11-11T07:20:01Z',
  envelope: {
    event: 'message',
    source: { session_id: 'b27ae7b3422dfd93c2e4dbe601af5293' },
    target: { session_id: 'cd3ecb7f657f17e27aef96ff92cf9312' },
  },
  content: { text: '本轮已交付 11 条 facts。要点：示例站点 CMS + nginx。' },
}

const participantAddressed: IOARecord = {
  seq: 6,
  ioa_message_id: 'f1e2d3',
  sender: '28cd3cd7aa11bb22cc33dd44ee55ff66',
  observed_at: '2026-11-11T07:15:00Z',
  envelope: {
    event: 'message',
    source: { session_id: 'b27ae7b3422dfd93c2e4dbe601af5293' },
    target: { session_id: 'pt_in_fb5e6d72' },
  },
  content: { text: '交叉情报（组件/敏感信息方向，已实测）。' },
}

const records = [commanderSay, workerReport, participantAddressed]
const maps = buildRecordMaps(sessions, people, records)

test('a [collab] prelude is split off the body, never shown as prose', () => {
  const parsed = parsePeerMessage(commanderSay.content!.text as string)
  assert.deepEqual(parsed.peer, {
    from: 'commander',
    to: 'pt_in_a484fc1d',
    kind: 'say',
    messageId: 'm_20f911f4',
    inReplyTo: '',
    facts: [],
  })
  assert.equal(parsed.body, '指挥官现场提示（已亲自验证）：目标可达。HTTP GET 返回 200。')
})

test('reply id and fact refs are parsed out of the prelude', () => {
  const parsed = parsePeerMessage('[collab] from=commander to=pt_in_x kind=result message_id=m_9 in_reply_to=m_1 facts=f_a,f_b\n正文\n第二行')
  assert.equal(parsed.peer?.inReplyTo, 'm_1')
  assert.deepEqual(parsed.peer?.facts, ['f_a', 'f_b'])
  assert.equal(parsed.body, '正文\n第二行')
})

test('CRLF, missing kind and a prelude without a body stay parseable', () => {
  assert.equal(parsePeerMessage('[collab] from=a to=b\r\nbody').body, 'body')
  assert.equal(parsePeerMessage('[collab] from=a to=b\r\nbody').peer?.kind, '')
  const empty = parsePeerMessage('[collab] from=a to=b')
  assert.equal(empty.peer?.messageId, '')
  assert.equal(empty.body, '')
})

test('foreign or malformed markers keep the text intact', () => {
  const plain = parsePeerMessage('普通汇报，没有协作头。')
  assert.equal(plain.peer, null)
  assert.equal(plain.body, '普通汇报，没有协作头。')
  const foreign = parsePeerMessage('[collab]garbage\nstill a body')
  assert.equal(foreign.peer, null)
  assert.equal(foreign.body, '[collab]garbage\nstill a body')
  const otherKeys = parsePeerMessage('[collab] foo=bar\nstill a body')
  assert.equal(otherKeys.peer, null)
  assert.equal(otherKeys.body, '[collab] foo=bar\nstill a body')
})

test('the commander message resolves to 指挥官 and its session, not to 未提供地址', () => {
  const view = describeRecord(commanderSay, maps)
  assert.equal(view.from?.label, '指挥官')
  assert.equal(view.from?.kind, 'commander')
  assert.match(view.fromDetail, /控制台直投/)
  assert.match(view.fromDetail, /from=commander/)
  assert.equal(view.to?.label, '侦察与资产测绘：portal.example')
  assert.equal(view.toDetail, '会话 9da4e9bb76…')
  assert.equal(view.peer?.kind, 'say')
  assert.equal(view.peer?.messageId, 'm_20f911f4')
})

test('a worker report shows its source session and resolves the reply target to 控制台', () => {
  const view = describeRecord(workerReport, maps)
  assert.equal(view.from?.label, '组件漏洞排查：portal.example')
  assert.equal(view.fromDetail, '会话 b27ae7b342…')
  assert.equal(view.to?.label, '控制台')
  assert.equal(view.to?.kind, 'control')
  assert.equal(view.toDetail, '控制台节点 cd3ecb7f65…')
  assert.equal(view.peer, null)
  assert.equal(view.body, '本轮已交付 11 条 facts。要点：示例站点 CMS + nginx。')
})

test('a participant id used as the IOA target resolves through the participant list', () => {
  const view = describeRecord(participantAddressed, maps)
  assert.equal(view.to?.label, '应用层测试：portal.example 主站与接口')
  assert.equal(view.toDetail, '参与者 pt_in_fb5e6d72')
})

test('missing addresses say so instead of inventing a name', () => {
  const bare: IOARecord = {
    seq: 99,
    ioa_message_id: 'x',
    observed_at: '2026-11-11T07:00:00Z',
    envelope: { event: 'inbox.route' },
    content: { text: '生命周期记录' },
  }
  const view = describeRecord(bare, maps)
  assert.equal(view.from, null)
  assert.match(view.fromDetail, /^未标注/)
  assert.equal(view.to, null)
  assert.match(view.toDetail, /^未标注/)
})

test('an unknown 32-hex address stays visibly unregistered', () => {
  const endpoint = resolveEndpoint('ffffffffffffffffffffffffffffffff', maps)
  assert.equal(endpoint.kind, 'unknown')
  assert.equal(endpoint.label, '未命名会话')
  assert.match(endpoint.hint, /不在当前项目的会话标签/)
})

test('commander, operator and participant ids resolve without a session label', () => {
  assert.equal(resolveEndpoint('commander', maps).label, '指挥官')
  assert.equal(resolveEndpoint('operator', maps).label, '操作员')
  assert.equal(resolveEndpoint('pt_in_a484fc1d', maps).kind, 'participant')
  assert.equal(resolveEndpoint('9da4e9bb76501b6b364c7345664e6c5a', maps).kind, 'session')
  assert.equal(resolveEndpoint('194adc7aca2b87015f6d4c5c472b242e', maps).kind, 'commander')
})

test('the control-node set comes from peer messages, not from any sender', () => {
  const onlyReports = buildRecordMaps(sessions, people, [workerReport])
  assert.equal(onlyReports.controlNodes.size, 0)
  assert.equal(resolveEndpoint('cd3ecb7f657f17e27aef96ff92cf9312', onlyReports).label, '未命名会话')
  assert.equal(maps.controlNodes.has('cd3ecb7f657f17e27aef96ff92cf9312'), true)
})

test('labels and short ids stay honest', () => {
  assert.equal(shortAddress('9da4e9bb76501b6b364c7345664e6c5a'), '9da4e9bb76…')
  assert.equal(shortAddress('abc'), 'abc')
  assert.equal(participantName('pt_in_a484fc1d', people), '侦察与资产测绘：portal.example')
  assert.equal(participantName('pt_in_unknown', people), 'in_unknown')
  assert.equal(participantName('commander', people), '指挥官')
  assert.equal(participantName('operator', people), '操作员')
  assert.equal(eventLabel('message'), '消息')
  assert.equal(eventLabel('subagent.delegate'), '子代理派发')
  assert.equal(eventLabel(''), '协议记录')
  assert.equal(eventLabel('custom.event'), 'custom.event')
  assert.equal(messageKindLabel('say'), '现场发言')
  assert.equal(messageKindLabel('question'), '提问')
  assert.equal(messageKindLabel('weird'), 'weird')
})
