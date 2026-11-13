import type { HostRuntimeStatus } from './types'

export function hostPresentation(host?: HostRuntimeStatus) {
  if (!host) return { status: 'idle', label: '状态未知', terminal: true } as const
  if (!host.online) return { status: 'error', label: '宿主未就绪', terminal: false } as const
  if (!host.ready) return { status: 'warning', label: '模型不可用 · 终端可用', terminal: true } as const
  return { status: 'online', label: '宿主与模型就绪', terminal: true } as const
}
