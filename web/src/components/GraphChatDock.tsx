import { MessageSquare, X } from 'lucide-react'
import { Button, ScrollArea } from '@cyber/ui'
import { ChatComposer } from '@/components/ChatComposer'
import { api } from '@/lib/api'
import { fmtTime } from '@/lib/format'
import { usePoll } from '@/lib/hooks'
import { participantName } from '@/lib/ioa'
import type { Message } from '@/lib/types'

/**
 * The 图谱 page's right dock: the operator talks to the commander / all
 * executors while looking at the map, so intervention lives next to the
 * battlefield instead of on a separate page. It shows the last few turns for
 * context and links to the full 通信 page for the whole record.
 */
export function GraphChatDock({
  paused,
  runningExecutors,
  onSent,
  onClose,
  onOpenConversation,
}: {
  paused: boolean
  runningExecutors: number
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
      className="absolute bottom-12 right-3 top-[56px] flex w-[340px] flex-col overflow-hidden rounded-lg border border-border bg-card/95 shadow-elevated backdrop-blur"
    >
      <div className="flex shrink-0 items-center gap-1.5 border-b border-border px-3 py-2">
        <MessageSquare className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="text-xs font-medium">下发</span>
        <span className="min-w-0 truncate text-[10px] text-muted-foreground">一次性提示 / 持续指令 / 换向</span>
        <Button size="icon-xs" variant="ghost" className="ml-auto shrink-0" title="收下方面板" onClick={onClose}>
          <X className="h-3 w-3" />
        </Button>
      </div>
      <ScrollArea className="min-h-0 flex-1">
        <div className="space-y-1 p-2">
          <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
            <span>最近对话</span>
            <button type="button" className="ml-auto text-primary hover:underline" onClick={onOpenConversation}>
              打开通信页
            </button>
          </div>
          {rows.length === 0 && !messages.loading && (
            <p className="px-0.5 py-1 text-[11px] text-muted-foreground">还没有消息；发第一条会出现在这里。</p>
          )}
          {messages.error && <p className="text-[11px] text-destructive">{messages.error}</p>}
          {rows.map((m) => (
            <div key={m.id} className="rounded border border-border/50 bg-muted/15 px-2 py-1">
              <div className="flex flex-wrap items-center gap-x-1.5 text-[10px] text-muted-foreground">
                <span className="text-info" title={`参与者 ${m.from_participant_id}`}>{participantName(m.from_participant_id, people)}</span>
                <span aria-hidden>→</span>
                <span className="text-info" title={`参与者 ${m.to_participant_id}`}>{participantName(m.to_participant_id, people)}</span>
                <span className="ml-auto shrink-0 font-mono">{fmtTime(m.created_at).slice(5)}</span>
              </div>
              <p className="tw-break mt-0.5 line-clamp-2 whitespace-pre-wrap text-[11px] leading-relaxed text-foreground/85">{m.text}</p>
            </div>
          ))}
        </div>
      </ScrollArea>
      <ChatComposer
        paused={paused}
        runningExecutors={runningExecutors}
        onSent={() => {
          messages.reload()
          onSent()
        }}
      />
    </div>
  )
}
