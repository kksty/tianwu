import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildGraph } from '../src/graph/model.ts'
import type { Intent } from '../src/lib/types.ts'

test('an unloaded premise stays attached to its intent instead of the project root', () => {
 const intent = {id:'in_child',title:'child',status:'pending',from_fact_ids:['old']} as Intent
 const graph = buildGraph([], [intent], [], undefined, [], {}, '', [{id:'fact:old',state:'not_loaded'}])
 assert.equal(graph.byId.get('f:old')?.type, 'boundary')
 assert.ok(graph.edges.some(e => e.source === 'f:old' && e.target === 's:in_child'))
 assert.ok(!graph.edges.some(e => e.source.startsWith('p:') && e.target === 's:in_child'))
})

// docs/35 §6 "not_loaded、missing、rejected 分别显示": a reference this page has
// not loaded is an expandable leaf, a reference the ledger no longer has is a
// dangling id, and a rejected reference exists but must not read as a live
// premise. The label and the carried state both have to keep them apart.
test('boundary nodes say why they are not drawn: not_loaded vs missing vs rejected', () => {
 const intent = {
   id: 'in_child',
   title: 'child',
   status: 'pending',
   from_fact_ids: ['loaded_elsewhere', 'gone', 'negated'],
 } as Intent
 const graph = buildGraph(
   [],
   [intent],
   [],
   undefined,
   [],
   {},
   '',
   [
     { id: 'fact:loaded_elsewhere', state: 'not_loaded' },
     { id: 'fact:gone', state: 'missing' },
     { id: 'fact:negated', state: 'rejected' },
   ],
 )
 const node = (id: string) => graph.byId.get(`f:${id}`)
 assert.equal(node('loaded_elsewhere')?.boundaryState, 'not_loaded')
 assert.match(node('loaded_elsewhere')?.label ?? '', /未载入/)
 assert.doesNotMatch(node('loaded_elsewhere')?.label ?? '', /引用缺失|已否定/)
 assert.equal(node('gone')?.boundaryState, 'missing')
 assert.match(node('gone')?.label ?? '', /引用缺失/)
 assert.equal(node('negated')?.boundaryState, 'rejected')
 assert.match(node('negated')?.label ?? '', /已否定/)
 // A boundary without a state from the server stays the plain expand leaf.
 const bare = buildGraph([], [{ ...intent, from_fact_ids: ['unknown'] } as Intent], [])
 assert.equal(bare.byId.get('f:unknown')?.boundaryState, undefined)
 assert.match(bare.byId.get('f:unknown')?.label ?? '', /展开/)
})
