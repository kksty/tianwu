import { test } from 'node:test'
import assert from 'node:assert/strict'
import { commandKind, filterCommands } from '../src/lib/tools.ts'

test('host command categories follow the protocol and include newly registered tools', () => {
  assert.equal(commandKind({ name: '!new-scanner' }), 'native')
  assert.equal(commandKind({ name: '/skill:custom' }), 'skill')
  assert.equal(commandKind({ name: '/compact' }), 'runtime')
})

test('catalogue search matches descriptions and usage within the selected category', () => {
  const commands = [
    { name: '!new-scanner', description: 'Web discovery', usage: '!new-scanner --target URL' },
    { name: '/skill:recon', description: 'Web discovery' },
  ]
  assert.deepEqual(filterCommands(commands, 'native', ' WEB '), [commands[0]])
  assert.deepEqual(filterCommands(commands, 'native', '--target'), [commands[0]])
  assert.deepEqual(filterCommands(commands, 'skill', 'web'), [commands[1]])
  assert.deepEqual(filterCommands(commands, 'runtime', ''), [])
})
