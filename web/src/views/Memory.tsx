import { useEffect, useState } from 'react'
import { Loader2, Save } from 'lucide-react'
import { Badge, Button, Callout, EmptyState, SkeletonList, Textarea } from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtBytes } from '@/lib/format'
import { useLoad } from '@/lib/hooks'

/**
 * The commander's notebook (state.md). It is an editor, not a viewer: one panel,
 * the file size and dirty state in the panel header, the save action as the
 * page's only primary button. Loading / error / empty are all explicit so a
 * slow state file never looks like an empty one.
 */
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
          {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
          {busy ? '保存中…' : '保存'}
        </Button>
      }
    >
      {state.error && (
        <Callout tone="destructive" className="mb-3">
          {state.error}
          <Button size="xs" variant="outline" className="ml-2" onClick={state.reload}>
            重试
          </Button>
        </Callout>
      )}

      {state.loading && (
        <Panel title="state.md">
          <SkeletonList count={6} />
        </Panel>
      )}

      {!state.loading && !state.error && !state.data && (
        <Panel bodyClassName="p-0">
          <EmptyState
            title="还没有记忆文件"
            description="指挥官每轮都会读到的笔记；它第一次写入后会出现在这里，你也可以手动创建。"
            action={
              <Button size="sm" variant="outline" onClick={state.reload}>
                重新读取
              </Button>
            }
          />
        </Panel>
      )}

      {state.data && (
        <Panel
          title="state.md"
          extra={
            <span className="flex flex-wrap items-center gap-2">
              {dirty && (
                <Badge size="sm" variant="warning">
                  未保存的修改
                </Badge>
              )}
              <span className="tw-nums font-mono text-[11px] text-ink-3">{fmtBytes(bytes)}</span>
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
          <div className="mt-2.5 space-y-1">
            <p className="text-[12px] leading-relaxed text-ink-2">
              这是指挥官每轮都会读到的笔记；它自己用 memory 工具追加/改写，人也可以在这里改。
            </p>
            {note && (
              <p role="status" className="text-[11px] leading-4 text-ink-3">
                {note}
              </p>
            )}
          </div>
        </Panel>
      )}
    </Page>
  )
}
