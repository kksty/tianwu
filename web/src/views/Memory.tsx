import { useEffect, useState } from 'react'
import { Save } from 'lucide-react'
import { Button, Callout, SkeletonList, Textarea } from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtBytes } from '@/lib/format'
import { useLoad } from '@/lib/hooks'

export function Memory() {
  const state = useLoad(api.state, [])
  const [text, setText] = useState('')
  const [dirty, setDirty] = useState(false)
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')

  useEffect(() => {
    if (state.data && !dirty) setText(state.data.state)
  }, [state.data, dirty])

  const bytes = new TextEncoder().encode(text).length

  async function save() {
    if (busy || !dirty || !state.data) return
    setBusy(true)
    setNote('')
    try {
      const res = await api.putState(text)
      // Publish the submitted snapshot before clearing dirty: the sync effect
      // must not restore the pre-save server text while reload is in flight.
      state.setData({ ...state.data, state: text })
      setNote(`已保存 ${res.bytes} 字节`)
      setDirty(false)
      state.reload()
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Page
      title="指挥官记忆"
      subtitle={state.data?.path ?? 'state.md'}
      actions={
        <Button size="sm" onClick={save} disabled={busy || !dirty || !state.data}>
          <Save className="h-3.5 w-3.5" /> {busy ? '保存中…' : '保存'}
        </Button>
      }
    >
      {state.loading && <SkeletonList count={4} />}
      {state.error && <Callout tone="destructive">{state.error}</Callout>}
      {state.data && (
        <Panel
          title="state.md"
          extra={
            <span className="font-mono text-[11px] text-muted-foreground">
              {fmtBytes(bytes)}
            </span>
          }
        >
          <Textarea
            aria-label="指挥官记忆内容"
            disabled={busy}
            value={text}
            onChange={(e) => {
              setText(e.target.value)
              setDirty(true)
            }}
            spellCheck={false}
            className="tw-mono min-h-[420px] text-[13px] leading-relaxed"
          />
          <div className="mt-2 text-[12px] text-muted-foreground">
            这是指挥官每轮都会读到的笔记；它自己用 memory 工具追加/改写，人也可以在这里改。
          </div>
          {note && <div className="mt-1 text-[12px] text-muted-foreground">{note}</div>}
        </Panel>
      )}
    </Page>
  )
}
