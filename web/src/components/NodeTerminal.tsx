import { useEffect, useState } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { X } from 'lucide-react'
import { Button } from '@cyber/ui'
import { projectID } from '@/lib/api'

/**
 * A PTY on an AOP node, embedded in the nodes page (no dialog). The host is
 * the `aiscan web` process listed in nodes.json; the console proxies its PTY
 * bytes. The host's own web UI is not embedded (D11).
 */
export function NodeTerminal({
  node,
  mode = 'shell',
  onClose,
}: {
  node: string
  mode?: 'shell' | 'repl'
  onClose?: () => void
}) {
  // A callback ref (not a ref object) so the effect runs after the pane has
  // attached: the panel mounts conditionally, and an effect keyed only on
  // `node` can run before the div exists.
  const [host, setHost] = useState<HTMLDivElement | null>(null)
  const [hint, setHint] = useState('连接中…')

  useEffect(() => {
    if (!node || !host) return
    setHint('连接中…')
    const term = new XTerm({
      convertEol: true,
      cursorBlink: true,
      fontSize: 12,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      theme: { background: '#0a0a0c', foreground: '#d4d4d8', cursor: '#d4d4d8' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host)
    fit.fit()

    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const project = projectID()
    const prefix = project ? `/p/${encodeURIComponent(project)}` : ''
    const url = `${proto}://${window.location.host}${prefix}/api/nodes/${encodeURIComponent(node)}/terminal?cols=${term.cols}&rows=${term.rows}&mode=${mode}`
    const socket = new WebSocket(url)
    socket.binaryType = 'arraybuffer'

    socket.onopen = () => {
      term.focus()
      fit.fit()
    }
    socket.onmessage = (event) => {
      if (typeof event.data === 'string') {
        try {
          const msg = JSON.parse(event.data) as { type?: string; message?: string }
          if (msg.type === 'status') {
            setHint(msg.message || '连接中…')
            return
          }
          if (msg.type === 'exit') {
            setHint('已断开')
            term.write(`\r\n\x1b[2m[${msg.message || '会话结束'}]\x1b[0m\r\n`)
          }
        } catch {
          /* a control frame we do not know: ignore it rather than corrupt the screen */
        }
        return
      }
      setHint('')
      term.write(new Uint8Array(event.data as ArrayBuffer))
    }
    socket.onclose = () => {
      setHint('已断开')
      term.write('\r\n\x1b[2m[连接已关闭]\x1b[0m\r\n')
    }

    const dataSub = term.onData((data) => {
      if (socket.readyState === WebSocket.OPEN) socket.send(new TextEncoder().encode(data))
    })
    const resizeSub = term.onResize(({ cols, rows }) => {
      if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: 'resize', cols, rows }))
    })
    const onResize = () => {
      try {
        fit.fit()
      } catch {
        /* the pane may already be unmounting */
      }
    }
    window.addEventListener('resize', onResize)
    const timer = window.setTimeout(() => fit.fit(), 50)

    return () => {
      window.clearTimeout(timer)
      window.removeEventListener('resize', onResize)
      dataSub.dispose()
      resizeSub.dispose()
      socket.close()
      term.dispose()
    }
  }, [node, host, mode])

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-[#0a0a0c]">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3">
        <span className="font-mono text-[12px] text-foreground">{node}</span>
        <span className="text-[11px] text-muted-foreground">{mode === 'repl' ? 'aiscan REPL' : '节点 shell'}</span>
        {hint && <span className="tw-break text-[11px] text-muted-foreground">· {hint}</span>}
        {onClose && (
          <Button size="icon-xs" variant="ghost" className="ml-auto" onClick={onClose} title="关闭终端">
            <X className="h-3.5 w-3.5" />
          </Button>
        )}
      </div>
      <div ref={setHost} className="min-h-0 flex-1 p-1" />
    </div>
  )
}
