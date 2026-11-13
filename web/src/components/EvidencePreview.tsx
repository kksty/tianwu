import { useEffect, useState } from 'react'
import { Download, FileWarning } from 'lucide-react'
import {
  Button,
  Callout,
  ScrollArea,
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SkeletonList,
} from '@cyber/ui'
import {
  EVIDENCE_PREVIEW_BYTES,
  contentRangeTotal,
  previewFormat,
  previewTruncated,
  type PreviewFile,
} from '@/lib/evidence-preview'
import { fmtBytes, fmtTime, shortID } from '@/lib/format'

/**
 * Clicking an evidence row opens this sheet: the bytes are read for the operator
 * instead of being pushed to the browser's download tray. Downloading is an
 * explicit second action (the header button), so "look at it" and "save it"
 * stop being the same click.
 *
 * It takes a PreviewFile rather than a ledger Evidence so the evidence page, the
 * graph inspector and the vulnerability library's copied evidence share one
 * reading surface. Evidence is untrusted by definition (a captured response
 * body, a dumped binary): nothing here is ever inserted as markup. Readable
 * content goes into a <pre> as text, images/pdf are handed to the browser's own
 * viewer, and inline HTML evidence shows as source — a preview must not execute
 * the target's script in the console origin.
 *
 * Layout: a fixed header (name / uri / type · size · time, size and time in
 * mono) over a body that owns the scroll, so the file identity never scrolls
 * away from the content it labels.
 */
export function EvidencePreview({
  file,
  onOpenChange,
}: {
  file: PreviewFile | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={!!file} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 sm:max-w-3xl">
        {file && <PreviewBody file={file} />}
      </SheetContent>
    </Sheet>
  )
}

type Preview =
  | { state: 'loading' }
  | { state: 'image' }
  | { state: 'pdf' }
  | { state: 'text'; text: string; truncated: boolean; mime?: string; total?: number }
  | { state: 'binary'; reason: string }
  | { state: 'error'; message: string }

function PreviewBody({ file }: { file: PreviewFile }) {
  const [preview, setPreview] = useState<Preview>({ state: 'loading' })

  useEffect(() => {
    const format = previewFormat(file)
    if (format !== 'other') {
      setPreview({ state: format })
      return
    }

    let alive = true
    const ctrl = new AbortController()
    setPreview({ state: 'loading' })

    void (async () => {
      try {
        const res = await fetch(file.contentURL, {
          // The server answers Range requests, so a 100 MB dump costs one MiB.
          headers: { Range: `bytes=0-${EVIDENCE_PREVIEW_BYTES - 1}` },
          signal: ctrl.signal,
        })
        if (res.status === 401) {
          const next = encodeURIComponent(window.location.pathname + window.location.hash)
          window.location.assign(`/login?next=${next}`)
          return
        }
        if (res.status !== 200 && res.status !== 206) {
          throw new Error(`服务端返回 HTTP ${res.status}`)
        }
        const buf = await res.arrayBuffer()
        if (!alive) return
        const text = decodeText(new Uint8Array(buf))
        if (text === null) {
          setPreview({
            state: 'binary',
            reason:
              '这份文件是二进制内容（或非文本编码），浏览器内不渲染，请下载后用本地工具查看。',
          })
          return
        }
        // A library copy has no ledger size; the Content-Range total is the
        // server's own measurement of the same file, so it can answer "is this
        // the whole thing" without inventing metadata.
        const total = contentRangeTotal(res.headers.get('Content-Range'))
        setPreview({
          state: 'text',
          text,
          truncated: previewTruncated(file.size, buf.byteLength, total),
          mime: res.headers.get('Content-Type') ?? undefined,
          total,
        })
      } catch (e) {
        if (!alive || ctrl.signal.aborted) return
        setPreview({ state: 'error', message: e instanceof Error ? e.message : String(e) })
      }
    })()

    return () => {
      alive = false
      ctrl.abort()
    }
    // The file's content URL is its identity: a different row is a different
    // preview, and the same row must not refetch on an unrelated re-render.
  }, [file.key]) // eslint-disable-line react-hooks/exhaustive-deps

  const mime = file.mime || (preview.state === 'text' ? preview.mime : undefined)
  const size = file.size ?? (preview.state === 'text' ? preview.total : undefined)

  return (
    <>
      <SheetHeader className="shrink-0 space-y-2 border-b border-hairline px-5 py-4 pr-12 text-left">
        <div className="tw-eyebrow">证据预览</div>
        <div className="flex items-start gap-3">
          <SheetTitle className="tw-break min-w-0 flex-1 font-mono text-[15px] leading-6 text-ink">
            {file.name}
          </SheetTitle>
          <Button asChild size="xs" variant="outline" className="shrink-0">
            <a href={file.downloadURL} download={file.name || true} title="下载原文件">
              <Download className="h-3 w-3" /> 下载
            </a>
          </Button>
        </div>
        <div className="tw-break font-mono text-[11px] leading-4 text-ink-3">{file.uri}</div>
        <dl className="flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-[11px] leading-4 text-ink-3">
          <div className="flex items-center gap-1">
            <dt className="sr-only">类型</dt>
            <dd className="tw-break">{mime || '未知类型'}</dd>
          </div>
          {size !== undefined && (
            <div className="flex items-center gap-1">
              <dt className="sr-only">大小</dt>
              <dd className="tw-nums">{fmtBytes(size)}</dd>
            </div>
          )}
          {file.createdAt && (
            <div className="flex items-center gap-1">
              <dt className="sr-only">时间</dt>
              <dd className="tw-nums">{fmtTime(file.createdAt)}</dd>
            </div>
          )}
          {file.intentID && (
            <div className="flex items-center gap-1">
              <dt>意图</dt>
              <dd className="truncate">{shortID(file.intentID)}</dd>
            </div>
          )}
          {file.runID && (
            <div className="flex items-center gap-1">
              <dt>run</dt>
              <dd className="truncate">{shortID(file.runID)}</dd>
            </div>
          )}
        </dl>
      </SheetHeader>

      <div className="flex min-h-0 flex-1 flex-col gap-2.5 overflow-hidden p-4">
        {preview.state === 'loading' && <SkeletonList count={8} />}

        {preview.state === 'error' && (
          <Callout tone="destructive">预览失败：{preview.message}</Callout>
        )}

        {preview.state === 'binary' && (
          <div className="space-y-3">
            <Callout tone="info" icon={<FileWarning className="h-3.5 w-3.5" />}>
              {preview.reason}
            </Callout>
            <Button asChild size="sm" variant="outline">
              <a href={file.downloadURL} download={file.name || true}>
                <Download className="h-3.5 w-3.5" /> 下载原文件
              </a>
            </Button>
          </div>
        )}

        {preview.state === 'image' && (
          <div className="tw-inset flex h-full min-h-[240px] items-center justify-center overflow-auto rounded-lg p-2">
            <img
              src={file.contentURL}
              alt={file.name || file.uri}
              className="max-h-full max-w-full object-contain"
              onError={() =>
                setPreview({ state: 'error', message: '浏览器无法显示这份图片（可能已损坏或格式不受支持）。' })
              }
            />
          </div>
        )}

        {preview.state === 'pdf' && (
          <iframe
            title={file.name || file.uri}
            src={file.contentURL}
            className="tw-inset h-full min-h-[60vh] w-full rounded-lg"
          />
        )}

        {preview.state === 'text' && (
          <>
            {preview.truncated && (
              <Callout tone="info" className="shrink-0">
                只预览前 {fmtBytes(EVIDENCE_PREVIEW_BYTES)}
                {size !== undefined ? `（共 ${fmtBytes(size)}）` : ''}；完整内容请下载原文件。
              </Callout>
            )}
            <ScrollArea className="tw-inset min-h-0 flex-1 rounded-lg">
              <pre className="tw-mono tw-break whitespace-pre-wrap p-3 text-[12px] leading-relaxed text-ink-2">
                {preview.text || '（空文件）'}
              </pre>
            </ScrollArea>
          </>
        )}
      </div>
    </>
  )
}

/**
 * Bytes → text, or null when the file is binary. The stored mime is not enough:
 * most captured HTTP bodies are registered as application/octet-stream, so the
 * content itself decides. NUL is the hard signal; a high share of C0 controls
 * means binary. UTF-8 is tried strictly, then GBK (the usual Chinese-target
 * response encoding) before giving up.
 */
function decodeText(bytes: Uint8Array): string | null {
  if (bytes.length === 0) return ''
  const sample = bytes.subarray(0, Math.min(bytes.length, 8192))
  let controls = 0
  for (const b of sample) {
    if (b === 0) return null
    if (b < 7 || (b > 13 && b < 32)) controls++
  }
  if (controls > sample.length * 0.02) return null

  const decode = (label: string, fatal: boolean): string | null => {
    try {
      return new TextDecoder(label, { fatal }).decode(bytes)
    } catch {
      return null
    }
  }
  return decode('utf-8', true) ?? decode('gbk', false)
}
