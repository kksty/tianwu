// Reading the console's native IOA conversation.
//
// A native observation is raw harness correspondence: the envelope says which
// address delivered to which address, and a peer message carries a `[collab]`
// prelude in front of its text so the receiving model knows who is speaking,
// how to reply and which ledger row it belongs to. The console must not show
// that prelude as prose: it is addressing metadata, the sentence after the
// newline is the body.
//
// Addresses are resolved against three maps, in this order: harness sessions
// (labelled with the intent title), project participants (commander /
// pt_in_… ids) and the control plane's own IOA nodes. Anything left over is an
// unregistered address and stays visibly unregistered instead of being dressed
// up as a name.

import type { IOARecord, Participant } from './types'

export interface PeerPrelude {
  from: string
  to: string
  kind: string
  messageId: string
  inReplyTo: string
  facts: string[]
}

// The prelude is exactly one line: `[collab] k=v k=v …`. A peer message with
// no trailing newline still parses; its body is then empty.
const preludePattern = /^\[collab\](?:[ \t]+([^\r\n]*))?(?:\r?\n|$)/

/** Split a peer message into its addressing prelude and the operator body. */
export function parsePeerMessage(text: string): { peer: PeerPrelude | null; body: string } {
  if (!text.startsWith('[collab]')) return { peer: null, body: text }
  const match = preludePattern.exec(text)
  if (!match) return { peer: null, body: text }
  const fields = new Map<string, string>()
  for (const token of (match[1] ?? '').split(/[ \t]+/)) {
    const eq = token.indexOf('=')
    if (eq <= 0) continue
    fields.set(token.slice(0, eq), token.slice(eq + 1))
  }
  const peer: PeerPrelude = {
    from: fields.get('from') ?? '',
    to: fields.get('to') ?? '',
    kind: fields.get('kind') ?? '',
    messageId: fields.get('message_id') ?? '',
    inReplyTo: fields.get('in_reply_to') ?? '',
    facts: (fields.get('facts') ?? '').split(',').map((f) => f.trim()).filter(Boolean),
  }
  // `[collab]` with no recognised field is somebody else's marker, not ours;
  // keep the text intact rather than silently eating its first line.
  if (!peer.from && !peer.to && !peer.kind && !peer.messageId) return { peer: null, body: text }
  return { peer, body: text.slice(match[0].length) }
}

export type EndpointKind = 'session' | 'participant' | 'commander' | 'control' | 'unknown'

/** One side of a record's direction, already resolved to something readable. */
export interface Endpoint {
  id: string
  label: string
  kind: EndpointKind
  /** Short form of the opaque id; never a substitute for the message id. */
  short: string
  /** Tooltip: what this address is and how it was resolved. */
  hint: string
}

export interface RecordMaps {
  sessions: Record<string, string>
  participants: Participant[]
  /** IOA node ids the control plane itself uses to deliver peer messages. */
  controlNodes: ReadonlySet<string>
}

/** Keep every opaque id on screen short but still comparable. */
export function shortAddress(id: string, n = 10): string {
  if (!id) return '—'
  return id.length > n ? `${id.slice(0, n)}…` : id
}

function sessionEndpoint(id: string, label: string): Endpoint {
  return {
    id,
    label,
    kind: label === '指挥官' ? 'commander' : 'session',
    short: shortAddress(id),
    hint: `harness 会话 ${id}（标签：${label}）`,
  }
}

function participantEndpoint(p: Pick<Participant, 'id' | 'title' | 'kind'>): Endpoint {
  return {
    id: p.id,
    label: p.title,
    kind: p.kind === 'commander' ? 'commander' : 'participant',
    short: shortAddress(p.id),
    hint: p.kind === 'commander' ? '参与者 commander（指挥官）' : `参与者 ${p.id}（方向：${p.title}）`,
  }
}

/**
 * Resolve one address. The order matters: a session label is the most precise
 * (it is the run the bytes landed in), a participant is the human-facing
 * identity, and a control node is the control plane itself — the worker→
 * commander replies arrive with that node as their IOA target.
 */
export function resolveEndpoint(id: string, maps: RecordMaps): Endpoint {
  const session = maps.sessions[id]
  if (session) return sessionEndpoint(id, session)
  const person = maps.participants.find((p) => p.id === id)
  if (person) return participantEndpoint(person)
  if (maps.controlNodes.has(id)) {
    return {
      id,
      label: '控制台',
      kind: 'control',
      short: shortAddress(id),
      hint: `控制台的 IOA 发送节点 ${id}：执行者把回传结果交回控制台，不指向某个具体会话`,
    }
  }
  if (id === 'commander') {
    return { id, label: '指挥官', kind: 'commander', short: 'commander', hint: '参与者 commander；当前会话标签里没有它' }
  }
  if (id === 'operator') {
    return { id, label: '操作员', kind: 'participant', short: 'operator', hint: '操作员（控制台人工输入）' }
  }
  if (id.startsWith('pt_')) {
    return {
      id,
      label: '未登记参与者',
      kind: 'unknown',
      short: shortAddress(id, 12),
      hint: `参与者 ${id} 不在当前项目的参与者列表里（可能已结束或被清理）`,
    }
  }
  return {
    id,
    label: '未命名会话',
    kind: 'unknown',
    short: shortAddress(id),
    hint: `IOA 地址 ${id} 不在当前项目的会话标签或参与者列表里（会话可能已结束或换轮）`,
  }
}

/**
 * Which IOA nodes belong to the control plane? Every peer message is formatted
 * by tianwu before it is sent, so a record whose body starts with `[collab]`
 * was sent by one of those nodes — and replies to the commander carry that
 * same node as their target, which is what turns an otherwise anonymous 32-hex
 * address into "控制台".
 */
export function buildRecordMaps(
  sessions: Record<string, string>,
  participants: Participant[],
  records: IOARecord[],
): RecordMaps {
  const controlNodes = new Set<string>()
  for (const rec of records) {
    if (!rec.sender) continue
    const text = typeof rec.content?.text === 'string' ? rec.content.text : ''
    if (text.startsWith('[collab]')) controlNodes.add(rec.sender)
  }
  return { sessions, participants, controlNodes }
}

export interface RecordView {
  peer: PeerPrelude | null
  body: string
  from: Endpoint | null
  to: Endpoint | null
  /** Readable explanation of the source address, shown as `来源：…`. */
  fromDetail: string
  /** Readable explanation of the target address, shown as `目标：…`. */
  toDetail: string
}

/** One-line operator explanation of a resolved address (`来源：…` / `目标：…`). */
export function endpointDetail(endpoint: Endpoint): string {
  switch (endpoint.kind) {
    case 'session':
      return `会话 ${endpoint.short}`
    case 'commander':
      return endpoint.id === 'commander' ? '参与者 commander' : '指挥官会话'
    case 'participant':
      return `参与者 ${endpoint.id}`
    case 'control':
      return `控制台节点 ${endpoint.short}`
    default:
      return `未登记地址 ${endpoint.short}`
  }
}

/**
 * Turn one raw record into the console's display model: readable direction,
 * separated body, and a plain-language note wherever the envelope alone does
 * not answer "who sent this to whom".
 */
export function describeRecord(rec: IOARecord, maps: RecordMaps): RecordView {
  const text = typeof rec.content?.text === 'string' ? rec.content.text : JSON.stringify(rec.content ?? {})
  const { peer, body } = parsePeerMessage(text)
  const sourceID = rec.envelope?.source?.session_id ?? ''
  const targetID = rec.envelope?.target?.session_id ?? ''

  let from = sourceID ? resolveEndpoint(sourceID, maps) : null
  let fromDetail = from ? endpointDetail(from) : ''
  if (!from && peer?.from) {
    from = resolveEndpoint(peer.from, maps)
    fromDetail = peer.from === 'commander' ? '控制台直投（消息头 from=commander）' : `控制台直投（from=${peer.from}）`
    from.hint = `${from.hint}；IOA 记录没有来源会话，发送方由消息头 from= 声明`
  } else if (!from) {
    fromDetail = rec.sender ? `未标注（IOA 发送节点 ${shortAddress(rec.sender, 8)}）` : '未标注（IOA 记录没有来源会话）'
  }

  let to = targetID ? resolveEndpoint(targetID, maps) : null
  let toDetail = to ? endpointDetail(to) : ''
  if (!to && peer?.to) {
    to = resolveEndpoint(peer.to, maps)
    toDetail = `控制台直投（消息头 to=${peer.to}）`
  } else if (!to) {
    toDetail = '未标注（IOA 记录没有目标会话）'
  }

  return { peer, body, from, to, fromDetail, toDetail }
}

/** Event kinds are protocol names; the console says what they mean in Chinese. */
export function eventLabel(event: string): string {
  switch (event) {
    case 'message':
      return '消息'
    case 'inbox.route':
      return '收件路由'
    case 'subagent.delegate':
      return '子代理派发'
    case 'subagent.return':
      return '子代理返回'
    case 'task.result':
      return '任务结果'
    case '':
      return '协议记录'
    default:
      return event
  }
}

/** `kind` is sender-declared message semantics, not a role or an enum. */
export function messageKindLabel(kind: string): string {
  switch (kind) {
    case 'instruction':
      return '指令变更'
    case 'say':
      return '现场发言'
    case 'question':
      return '提问'
    case 'update':
      return '进展更新'
    case 'result':
      return '结果回传'
    case 'task_notice':
      return '局部任务结果'
    case 'note':
      return '备注'
    default:
      return kind
  }
}

/** Human-facing name for a project participant id, with the id as fallback. */
export function participantName(id: string, people: Participant[]): string {
  if (!id) return '—'
  const person = people.find((p) => p.id === id)
  if (person) return person.title
  if (id === 'commander') return '指挥官'
  if (id === 'operator') return '操作员'
  return id.replace(/^pt_/, '')
}
