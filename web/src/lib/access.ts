import type { AccessEntry } from './types.ts'

export function canOperateBrowser(entry: AccessEntry): boolean {
  return entry.kind === 'browser' && entry.lease?.state === 'held' && entry.lease.owner === 'operator' && !entry.lease.run_id && !entry.lease.busy
}

export function browserOccupancyLabel(entry: AccessEntry): string {
  if (!entry.lease) return '空闲'
  if (entry.lease.busy) return '操作进行中'
  if (entry.lease.state === 'recovery') return '待确认恢复'
  return entry.lease.owner === 'operator' ? '操作员占用' : '执行者占用'
}
