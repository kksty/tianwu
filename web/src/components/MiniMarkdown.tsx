import { Fragment } from 'react'

/**
 * A deliberately small markdown reader for report files: headings, list items,
 * fenced code, quotes and paragraphs. Everything is rendered as text nodes, so a
 * report written by a model cannot inject markup into the console.
 *
 * Rhythm is fixed by the design system: headings 13–15px, body 13px, and the
 * vertical gap between blocks is 8px inside a section / 12px between sections,
 * so a long report reads as scannable sections instead of one grey wall.
 */
export function MiniMarkdown({ text }: { text: string }) {
  const src = text.split(/\r?\n/)
  const out: React.ReactNode[] = []
  let list: { ordered: boolean; items: string[] } | null = null
  let code: string[] | null = null
  let quote: string[] | null = null

  const flushList = (key: string) => {
    if (!list) return
    const { ordered, items } = list
    const cls =
      'my-2 space-y-1 pl-5 text-[13px] leading-relaxed text-ink-2 marker:text-ink-3'
    out.push(
      ordered ? (
        <ol key={key} className={`${cls} list-decimal`}>
          {items.map((li, i) => (
            <li key={i} className="tw-break pl-0.5">
              {li}
            </li>
          ))}
        </ol>
      ) : (
        <ul key={key} className={`${cls} list-disc`}>
          {items.map((li, i) => (
            <li key={i} className="tw-break pl-0.5">
              {li}
            </li>
          ))}
        </ul>
      ),
    )
    list = null
  }

  const flushQuote = (key: string) => {
    if (!quote) return
    out.push(
      <blockquote
        key={key}
        className="my-2 border-l-2 border-hairline pl-3 text-[13px] leading-relaxed text-ink-2"
      >
        {quote.map((line, i) => (
          <p key={i} className="tw-break">
            {line}
          </p>
        ))}
      </blockquote>,
    )
    quote = null
  }

  const flushCode = (key: string) => {
    if (!code) return
    out.push(
      <pre
        key={key}
        className="tw-mono tw-break tw-inset my-3 overflow-x-auto whitespace-pre-wrap rounded-lg p-3 text-[12px] leading-relaxed text-ink-2"
      >
        {code.join('\n')}
      </pre>,
    )
    code = null
  }

  const flushAll = (idx: number) => {
    flushList(`l${idx}`)
    flushQuote(`q${idx}`)
  }

  for (let idx = 0; idx < src.length; idx++) {
    const raw = src[idx]
    const line = raw.trimEnd()

    if (line.startsWith('```')) {
      flushAll(idx)
      if (code) flushCode(`c${idx}`)
      else code = []
      continue
    }
    if (code) {
      code.push(raw)
      continue
    }

    // Block quote: `> …`, one paragraph per quoted line.
    const bq = /^>\s?(.*)$/.exec(line)
    if (bq) {
      flushList(`l${idx}`)
      if (!quote) quote = []
      quote.push(bq[1])
      continue
    }
    flushQuote(`q${idx}`)

    const h = /^(#{1,4})\s+(.*)$/.exec(line)
    if (h) {
      flushAll(idx)
      const level = h[1].length as 1 | 2 | 3 | 4
      const cls =
        level === 1
          ? 'mt-4 mb-2 text-[15px] font-semibold leading-6 tracking-tight text-ink'
          : level === 2
            ? 'mt-4 mb-1.5 text-[14px] font-semibold leading-5 text-ink'
            : 'mt-3 mb-1 text-[13px] font-semibold leading-5 text-ink-2'
      out.push(
        <div key={`h${idx}`} className={cls}>
          {h[2]}
        </div>,
      )
      continue
    }

    const li = /^\s*[-*]\s+(.*)$/.exec(line)
    if (li) {
      if (!list || list.ordered) {
        flushList(`l${idx}`)
        list = { ordered: false, items: [] }
      }
      list.items.push(li[1])
      continue
    }

    const oli = /^\s*\d+[.)]\s+(.*)$/.exec(line)
    if (oli) {
      if (!list || !list.ordered) {
        flushList(`l${idx}`)
        list = { ordered: true, items: [] }
      }
      list.items.push(oli[1])
      continue
    }

    if (/^\s*(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
      flushAll(idx)
      out.push(<hr key={`hr${idx}`} className="my-3 border-hairline" />)
      continue
    }

    if (line.trim() === '') {
      flushAll(idx)
      continue
    }

    flushAll(idx)
    out.push(
      <p key={`p${idx}`} className="tw-break my-2 text-[13px] leading-relaxed text-ink-2">
        {line}
      </p>,
    )
  }
  flushAll(src.length)
  flushCode('ctail')

  return <Fragment>{out}</Fragment>
}
