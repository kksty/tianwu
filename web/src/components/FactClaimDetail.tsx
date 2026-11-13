// Fact detail: assertion identity and review provenance.
//
// A fact row is not "verified" as a whole: the claim (title/doc/evidence/…)
// has a version, and each review proves one version. This component shows both
// sides side by side and calls out the state that a single badge would hide —
// "a pass reviewed an older assertion, the current assertion is unverified".
// The state is derived from reviews[] + fact.claim_version (lib/claim.ts); no
// backend field is invented.
//
// Visual role: a supporting detail block, not a page. It uses the quiet half of
// the type ramp (11–12px, ink-2/ink-3) so the fact title above it keeps the
// weight, and every review is one flat hairline row instead of a stack of cards.
import { useMemo } from 'react'
import { Badge, Callout, StatusIndicator } from '@cyber/ui'
import { fmtTime, statusKind, statusLabel } from '@/lib/format'
import {
  claimReviewSummary,
  claimVersionLabel,
  reviewCoversCurrentClaim,
  reviewVersionLabel,
  sortReviewsNewestFirst,
} from '@/lib/claim'
import type { Fact, Review } from '@/lib/types'

export function FactClaimDetail({
  fact,
  reviews = [],
}: {
  fact: Fact
  reviews?: Review[]
}) {
  const summary = useMemo(() => claimReviewSummary(fact, reviews), [fact, reviews])
  const sortedReviews = useMemo(() => sortReviewsNewestFirst(reviews), [reviews])
  const needsAttention =
    summary.state === 'stale' || summary.state === 'changed' || summary.state === 'unbound'
  const shownDetail =
    summary.state === 'unreviewed' ||
    needsAttention ||
    summary.state === 'verified' ||
    summary.state === 'rejected' ||
    summary.state === 'needs_evidence'

  return (
    <div className="space-y-2.5" data-fact-claim-detail>
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="tw-eyebrow">断言与复核</span>
        <Badge
          size="sm"
          variant="secondary"
          title="claim_version：断言内容每次变化 +1；只改标签/来源等展示信息不变版本"
        >
          断言 {claimVersionLabel(summary.version)}
        </Badge>
        <Badge
          size="sm"
          variant={
            summary.state === 'verified'
              ? 'success'
              : summary.state === 'rejected'
                ? 'danger'
                : needsAttention
                  ? 'warning'
                  : 'muted'
          }
        >
          {summary.label}
        </Badge>
      </div>

      {needsAttention ? (
        <Callout tone="warning">{summary.detail}</Callout>
      ) : shownDetail ? (
        <div className="text-[12px] leading-relaxed text-ink-2">{summary.detail}</div>
      ) : null}

      <div className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-[12px]">
        <ClaimMeta
          label="声明"
          value={fact.declaration_id || '未记录'}
          mono
          title={fact.declaration_id || '这条事实没有声明身份（旧写入或派生事实）'}
        />
        <ClaimMeta
          label="观察于"
          value={fact.observed_at ? fmtTime(fact.observed_at) : '未记录'}
          title="observed_at：观察发生的时刻，不是入账时刻"
        />
        <ClaimMeta
          label="执行现场"
          value={fact.execution_context_ref || '未记录'}
          mono
          title={fact.execution_context_ref || '没有记录执行现场引用'}
        />
        <ClaimMeta
          label="断言指纹"
          value={fact.claim_hash ? `${fact.claim_hash.slice(0, 12)}…` : '未记录'}
          mono
          title={fact.claim_hash || '没有记录断言指纹'}
        />
      </div>

      <div className="flex flex-wrap items-center gap-2 border-t border-hairline pt-2.5">
        <span className="tw-eyebrow">复核（{sortedReviews.length}）</span>
        {summary.proved && (
          <span className="font-mono text-[11px] tw-nums text-ink-3" title="最近一次通过结论所证明的断言版本">
            最近通过 {reviewVersionLabel(summary.proved.review)}
          </span>
        )}
      </div>
      {sortedReviews.length === 0 ? (
        <div className="text-[12px] text-ink-3">没有复核记录</div>
      ) : (
        <div className="space-y-1.5">
          {sortedReviews.map((review) => {
            const covers = reviewCoversCurrentClaim(review, fact)
            return (
              <div
                key={review.id}
                className="rounded-md border border-hairline bg-surface-2/40 px-2.5 py-1.5"
              >
                <div className="flex flex-wrap items-center gap-1.5">
                  <StatusIndicator
                    compact
                    status={statusKind(review.verdict)}
                    label={statusLabel(review.verdict)}
                    className="shrink-0"
                  />
                  <Badge
                    size="sm"
                    variant={covers ? 'primary' : 'muted'}
                    title={covers ? '这条复核证明的就是当前断言' : '这条复核证明的是更早的断言版本'}
                  >
                    证明 {reviewVersionLabel(review)}
                  </Badge>
                  {!covers && (
                    <Badge size="sm" variant="warning">
                      旧断言
                    </Badge>
                  )}
                  <span className="ml-auto font-mono text-[11px] tw-nums text-ink-3">
                    {fmtTime(review.created_at)}
                  </span>
                </div>
                {review.notes && (
                  <div className="tw-break mt-1 text-[12px] leading-relaxed text-ink-2">
                    {review.notes}
                  </div>
                )}
                {review.verifier_run && (
                  <div className="truncate font-mono text-[11px] text-ink-3" title={review.verifier_run}>
                    复核 run {review.verifier_run}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

function ClaimMeta({
  label,
  value,
  mono = false,
  title,
}: {
  label: string
  value: string
  mono?: boolean
  title?: string
}) {
  return (
    <>
      <span className="text-[11px] leading-5 text-ink-3" title={title}>
        {label}
      </span>
      <span
        className={`tw-break min-w-0 text-[12px] leading-5 text-ink-2 ${mono ? 'font-mono' : ''}`}
        title={title}
      >
        {value}
      </span>
    </>
  )
}
