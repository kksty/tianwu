import {test} from 'node:test'
import assert from 'node:assert/strict'
import {buildGraph} from '../src/graph/model.ts'
import type {Intent} from '../src/lib/types.ts'

test('an edge page creates both unloaded endpoints before their node pages arrive',()=>{
 const g=buildGraph([],[],[],undefined,[],{},'',[],[{source:'fact:old',target:'intent:next',kind:'premise'}])
 assert.equal(g.byId.get('f:old')?.type,'boundary')
 assert.equal(g.byId.get('s:next')?.type,'boundary')
 assert.ok(g.edges.some(e=>e.source==='f:old'&&e.target==='s:next'))
})
test('deferred large prerequisite lists do not invent a project-root relationship',()=>{
 const g=buildGraph([],[{id:'in_task',title:'task',status:'pending'} as Intent],[],undefined,[],{},'',[],[],[{id:'intent:in_task',omitted:['from_fact_ids']}])
 assert.ok(!g.edges.some(e=>e.source.startsWith('p:')&&e.target==='s:in_task'))
})
