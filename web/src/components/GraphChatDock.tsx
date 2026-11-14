import { MessageSquare, X } from 'lucide-react'
import { Button, ScrollArea } from '@cyber/ui'
import { ChatComposer } from '@/components/ChatComposer'
import { api } from '@/lib/api'
import { fmtTime } from '@/lib/format'
import { usePoll } from '@/lib/hooks'
import { participantName } from '@/lib/ioa'
import type { Message } from '@/lib/types'
import { DockResizeHandle, type DockSize } from './graph/DockResize'

/**
 * The 图谱 page's right dock: the operator talks to the commander / all
 * executors while looking at the map, so intervention lives next to the
 * battlefield instead of on a separate page. It shows the last few turns for
 * context and links to the full 通信 page for the whole record.
 */
export function GraphChatDock({
  paused,
  runningExecutors,
  dock,
  onSent,
  onClose,
  onOpenConversation,
}: {
  paused: boolean
  runningExecutors: number
  /** Shared width + drag glue for the graph's right-hand overlays. */
  dock: DockSize
  /** Refresh the board after a send (the dock refreshes its own feed). */
  onSent: () => void
  onClose: () => void
  onOpenConversation: () => void
}) {
  // Only mounted while the dock is open, so the graph page does not poll the
  // message ledger when the operator is just looking at the map.
  const messages = usePoll(api.messages, 4000)
  const participants = usePoll(api.participants, 8000)
  const people = participants.data ?? []
  const rows: Message[] = (messages.data ?? []).slice(-6).reverse()

  return (
    <div
      data-graph-chat
      className="absolute bottom-12 right-3 top-[56px] flex max-w-[calc(100vw-1.5rem)] flex-col overflow-hidden rounded-xl border border-hairline bg-card shadow-elevated"
      style={{ width: dock.width }}
    >
      <DockResizeHandle dock={dock} />
      <div className="flex shrink-0 items-center gap-2 border-b border-hairline bg-surface-1/60 px-4 py-2.5">
        <MessageSquare className="h-4 w-4 shrink-0 text-ink-3" />
        <div className="min-w-0">
          <div className="tw-section-title">最近对话</div>
          <div className="truncate text-[11px] leading-4 text-ink-3">最近 6 条 · 完整记录在通信页</div>
        </div>
        <button
          type="button"
          className="ml-auto shrink-0 rounded text-[11px] text-primary transition-colors hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          onClick={onOpenConversation}
        >
          打开通信页
        </button>
        <Button
          size="icon-sm"
          variant="ghost"
          className="shrink-0"
          title="收起下发面板"
          aria-label="收起下发面板"
          onClick={onClose}
        >
          <X className="h-4 w-4" />
        </Button>
      </div>
      <ScrollArea className="min-h-0 flex-1 bg-surface-1">
        <div className="space-y-1.5 p-2.5">
          {rows.length === 0 && !messages.loading && (
            <div className="rounded-lg border border-dashed border-hairline px-3 py-4 text-center text-[12px] leading-relaxed text-ink-3">
              还没有消息；发第一条会出现在这里。
            </div>
          )}
          {messages.error && <p className="text-[12px] text-destructive">{messages.error}</p>}
          {rows.map((m) => (
            <div
              key={m.id}
              className="rounded-lg border border-hairline bg-surface-2/60 px-2.5 py-2 transition-colors hover:bg-surface-2"
            >
              <div className="flex flex-wrap items-center gap-x-1.5 text-[11px] text-ink-3">
                <span className="text-info" title={`参与者 ${m.from_participant_id}`}>
                  {participantName(m.from_participant_id, people)}
                </span>
                <span aria-hidden>→</span>
                <span className="text-info" title={`参与者 ${m.to_participant_id}`}>
                  {participantName(m.to_participant_id, people)}
                </span>
                <span className="tw-nums ml-auto shrink-0 font-mono">{fmtTime(m.created_at).slice(5)}</span>
              </div>
              <p className="tw-break mt-1 line-clamp-2 whitespace-pre-wrap text-[12px] leading-relaxed text-ink-2">
                {m.text}
              </p>
            </div>
          ))}
        </div>
      </ScrollArea>
      <div className="shrink-0 bg-card">
        <ChatComposer
          paused={paused}
          runningExecutors={runningExecutors}
          onManageInstructions={onOpenConversation}
          manageLabel="在通信页管理"
          onSent={() => {
            messages.reload()
            onSent()
          }}
        />
      </div>
    </div>
  )
}
