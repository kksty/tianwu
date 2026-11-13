import { Fragment } from 'react'

/**
 * A deliberately small markdown reader for report files: headings, list items,
 * fenced code and paragraphs. Everything is rendered as text nodes, so a report
 * written by a model cannot inject markup into the console.
 */
export function MiniMarkdown({ text }: { text: string }) {
  const src = text.split(/\r?\n/)
  const out: React.ReactNode[] = []
  let list: string[] | null = null
  let code: string[] | null = null

  const flushList = (key: string) => {
    if (!list) return
    out.push(
      <ul key={key} className="my-1 list-disc space-y-0.5 pl-5 text-[13px] text-foreground/90">
        {list.map((li, i) => (
          <li key={i} className="tw-break">
            {li}
          </li>
        ))}
      </ul>,
    )
    list = null
  }

  for (let idx = 0; idx < src.length; idx++) {
    const raw = src[idx]
    const line = raw.trimEnd()

    if (line.startsWith('```')) {
      flushList(`l${idx}`)
      if (code) {
        out.push(
          <pre
            key={`c${idx}`}
            className="tw-mono my-1 overflow-x-auto rounded border border-border bg-muted/40 p-2 text-[12px] leading-relaxed"
          >
            {code.join('\n')}
          </pre>,
        )
        code = null
      } else {
        code = []
      }
      continue
    }
    if (code) {
      code.push(raw)
      continue
    }

    const h = /^(#{1,4})\s+(.*)$/.exec(line)
    if (h) {
      flushList(`l${idx}`)
      const level = h[1].length
      const cls =
        level === 1
          ? 'mt-3 mb-1 text-h4 font-semibold text-foreground'
          : level === 2
            ? 'mt-3 mb-1 text-ui font-semibold text-foreground'
            : 'mt-2 mb-0.5 text-[13px] font-semibold text-foreground/90'
      out.push(
        <div key={`h${idx}`} className={cls}>
          {h[2]}
        </div>,
      )
      continue
    }

    const li = /^\s*[-*]\s+(.*)$/.exec(line)
    if (li) {
      if (!list) list = []
      list.push(li[1])
      continue
    }

    if (line.trim() === '') {
      flushList(`l${idx}`)
      continue
    }

    flushList(`l${idx}`)
    out.push(
      <p key={`p${idx}`} className="tw-break my-0.5 text-[13px] leading-relaxed text-foreground/85">
        {line}
      </p>,
    )
  }
  flushList('tail')
  if (code) {
    out.push(
      <pre
        key="ctail"
        className="tw-mono my-1 overflow-x-auto rounded border border-border bg-muted/40 p-2 text-[12px]"
      >
        {code.join('\n')}
      </pre>,
    )
  }

  return <Fragment>{out}</Fragment>
}
