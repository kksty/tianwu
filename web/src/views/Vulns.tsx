import { useEffect, useState, type ReactNode } from 'react'
import { Bug, Download, ExternalLink, Eye, Pause, Play, Search, ShieldCheck, Waypoints } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Chip,
  EmptyState,
  Input,
  ScrollArea,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SkeletonList,
  StatusIndicator,
} from '@cyber/ui'
import { cn } from '@cyber/theme'
import { MiniMarkdown } from '@/components/MiniMarkdown'
import { EvidencePreview } from '@/components/EvidencePreview'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { vulnEvidencePreviewFile, type PreviewFile } from '@/lib/evidence-preview'
import { fmtTime, oneLine, statusKind, statusLabel } from '@/lib/format'
import { useConsoleLocation, useDebounced, useLoad } from '@/lib/hooks'
import { navigate } from '@/lib/navigation'
import type { ProjectInfo, VulnEntry, VulnReview } from '@/lib/types'

/** Select sentinel: Radix forbids an empty value, "" means "no filter". */
const ALL_PROJECTS = '__all'

/**
 * The status filter is a reading aid over the already-loaded page, not a second
 * query: the list endpoint already returns every status, and refetching for a
 * chip would hide the counts that make the chips useful. "Severity" is not a
 * field of the vulnerability library (a vuln fact carries confidence + proof),
 * so the filters that exist are project, status and free-text search.
 */
const STATUS_FILTERS = [
  { value: 'unverified', label: '未复核' },
  { value: 'verified', label: '已复核' },
  { value: 'rejected', label: '已否决' },
] as const

/**
 * The vulnerability library has one backing store (`data/vulns`, D34) and two
 * readers:
 *
 * - `/g/?view=vulns` — the instance library, filterable by project. The filter
 *   lives in the URL (`?project=<id>`) so it is linkable.
 * - `/p/<id>/?view=vulns` — the project page, which is the same library locked
 *   to that project. It is a filtered read, not a per-project copy: the library
 *   stays outside the project, so deleting the project leaves its entries in the
 *   global library as `orphaned`, visible only from the instance page.
 *
 * A row is a summary, not the whole conclusion. The list response already
 * carries the full doc (repro command, request/response packets), the evidence
 * copies and the latest verifier verdict, so the detail sheet needs no extra
 * request — "what the row says" and "what the detail says" can never disagree.
 * The open row is named in the URL (`?vuln=<library id>`): the view stays
 * linkable and headless verification can open one directly.
 */
export function Vulns({
  projects = [],
  lockedProject = '',
}: {
  projects?: ProjectInfo[]
  lockedProject?: string
}) {
  const locked = lockedProject !== ''
  const [q, setQ] = useState('')
  const query = useDebounced(q, 250)
  const [status, setStatus] = useState('')
  const location = useConsoleLocation()
  const params = new URLSearchParams(location.search)
  const project = params.get('project') ?? ''
  const scope = locked ? lockedProject : project
  const list = useLoad(
    () => api.vulns({ q: query, project: scope || undefined, limit: 200 }),
    [query, scope],
  )
  const rows = list.data?.vulns ?? []
  const total = list.data?.total ?? 0
  const matched = list.data?.matched ?? rows.length
  const unverified = rows.filter((v) => v.status === 'unverified').length
  const visible = status ? rows.filter((v) => v.status === status) : rows
  const statusCount = (value: string) => rows.filter((v) => v.status === value).length

  // 详情用库内 id 记录「打开的是哪一条」，再从最新的 rows 派生：批量/复核轮询刷新
  // 后，面板会自己换成新的复核结论，而不是停在打开那一刻的快照。
  const selectedId = params.get('vuln') ?? ''
  const selected = selectedId ? (rows.find((v) => v.id === selectedId) ?? null) : null

  // 证据副本点开走与「证据」页同一个预览面板：阅读不是下载，也不离开漏洞页。
  // 库内证据只有库内相对路径，没有 ledger 行，所以由路径构造预览来源。
  const [evidencePreview, setEvidencePreview] = useState<PreviewFile | null>(null)

  // 行内复核：点击只是排一次真实的复核 run，不是改状态，所以按 id 记录请求
  // 在途/已接受；已接受的显示「已排队」，在跑完之前状态仍是未复核。
  const [verifyRows, setVerifyRows] = useState<Record<string, 'busy' | 'queued'>>({})
  const [verifyError, setVerifyError] = useState('')
  // 头部「一键复核」只在列表收窄到一个项目时出现：批量接口是项目作用域的，
  // 实例级总库没有 /p/<id> 前缀。
  const [batchNote, setBatchNote] = useState('')
  const [batchBusy, setBatchBusy] = useState(false)
  const [batchActive, setBatchActive] = useState(false)

  // 复核开关与项目暂停是两个独立开关（D137）：项目暂停时仍可复核，复核暂停只停
  // 复核、不影响指挥官与执行者。只有项目页有单项目上下文，实例库不显示这个开关。
  const reviews = useLoad(
    () =>
      locked
        ? api.verifierStatus()
        : Promise.resolve({ enabled: false, paused: false }),
    [locked, lockedProject],
  )
  const reviewsPaused = locked && (reviews.data?.paused ?? false)
  const [reviewBusy, setReviewBusy] = useState(false)
  const [reviewNote, setReviewNote] = useState('')

  async function toggleReviews() {
    if (!locked || reviewBusy) return
    setReviewBusy(true)
    setVerifyError('')
    setReviewNote('')
    try {
      if (reviewsPaused) {
        await api.resumeReviews()
        setReviewNote('复核已继续：项目暂停状态和指挥官不受影响')
      } else {
        const r = await api.pauseReviews()
        setReviewNote(
          r.canceled > 0
            ? `已暂停复核：正在跑的 ${r.canceled} 条复核已取消；项目与指挥官不受影响`
            : '已暂停复核：项目与指挥官不受影响',
        )
      }
      reviews.reload()
      list.reload()
    } catch (e) {
      setVerifyError(`切换复核状态失败：${e instanceof Error ? e.message : String(e)}`)
    } finally {
      setReviewBusy(false)
    }
  }

  // 批量复核 active 期间每 20 秒重拉列表，让状态变化自然出现；全部复核完
  // （没有未复核行）自动停；卸载或换筛选时清定时器。
  useEffect(() => {
    if (!batchActive) return
    const timer = window.setInterval(() => list.reload(), 20000)
    return () => window.clearInterval(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [batchActive, scope])

  useEffect(() => {
    if (!batchActive || list.loading) return
    if (rows.length > 0 && unverified === 0) setBatchActive(false)
  }, [batchActive, list.loading, rows.length, unverified])

  async function verifyRow(v: VulnEntry) {
    const id = v.id
    if (verifyRows[id] || !v.project_id) return
    if (reviewsPaused) {
      setVerifyError('复核已暂停：点顶部「继续复核」后再复核；项目暂停不影响复核')
      return
    }
    setVerifyError('')
    setVerifyRows((cur) => ({ ...cur, [id]: 'busy' }))
    try {
      await api.verifyFactInProject(v.project_id, v.source_id)
      setVerifyRows((cur) => ({ ...cur, [id]: 'queued' }))
      // The verifier run will flip the status when it finishes; poll while it is
      // outstanding instead of promising an immediate change.
      setBatchActive(true)
      list.reload()
    } catch (e) {
      setVerifyRows((cur) => {
        const next = { ...cur }
        delete next[id]
        return next
      })
      setVerifyError(`复核「${v.title}」失败：${e instanceof Error ? e.message : String(e)}`)
    }
  }

  async function verifyAll() {
    if (!scope || batchBusy) return
    setBatchBusy(true)
    setVerifyError('')
    setBatchNote('')
    try {
      const r = await api.verifyPendingFacts(scope)
      setBatchNote(`已排入 ${r.queued} 条复核（${r.skipped} 条已在复核中）`)
      setBatchActive(true)
      list.reload()
    } catch (e) {
      setVerifyError(e instanceof Error ? e.message : String(e))
    } finally {
      setBatchBusy(false)
    }
  }

  /** 详情是 URL 的一部分：?vuln=<库内 id> 可分享、可被无头验证直接打开。 */
  function writeVulnParam(id: string) {
    const params = new URLSearchParams(window.location.search)
    // view 必须一起写：从全局 shell 刷新时才能落回漏洞库而不是态势页。
    params.set('view', 'vulns')
    if (id) params.set('vuln', id)
    else params.delete('vuln')
    navigate(`${window.location.pathname}?${params.toString()}`, true)
  }

  function openVuln(v: VulnEntry) {
    writeVulnParam(v.id)
  }

  function closeVuln() {
    writeVulnParam('')
  }

  // 搜索或刷新把打开的那条筛出结果时，Sheet 会自己关掉，但 URL 里还记着一个
  // 已经不存在的 id：复制出去的链接、刷新后的页面都会指向一条打不开的条目。
  // 列表已经给出结果（list.data）且找不到该 id 时，指针一起清掉。
  useEffect(() => {
    if (!selectedId || selected || !list.data) return
    closeVuln()
    // closeVuln 只是写 URL 参数，不依赖本次渲染的其它状态。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedId, selected, list.data])

  function pickProject(next: string) {
    if (locked) return
    // 换筛选后原来打开的那条可能不在结果里，指针不能留在 URL 上。
    const params = new URLSearchParams(window.location.search)
    params.set('view', 'vulns')
    params.delete('vuln')
    if (next) params.set('project', next)
    else params.delete('project')
    navigate(`${window.location.pathname}?${params.toString()}`, true)
  }

  const subtitle = locked
    ? `本项目已入库 ${matched} 条 · 与全局漏洞库同一份数据，状态跟随复核`
    : matched === total
      ? `实例级：声明即入库，删项目保留条目与证据副本（共 ${total} 条）`
      : `实例级：声明即入库，删项目保留条目与证据副本（全库 ${total} 条，当前筛选 ${matched} 条）`

  const canBatch = scope !== ''
  const hasHeaderActions = locked || canBatch

  return (
    <Page
      title={locked ? '漏洞' : '漏洞库'}
      subtitle={subtitle}
      actions={
        hasHeaderActions ? (
          <div className="flex flex-wrap items-center gap-2">
            {locked && (
              <>
                {reviewsPaused && (
                  <Badge
                    size="sm"
                    variant="warning"
                    title="复核开关独立于项目暂停：暂停复核不会恢复指挥官，项目暂停时仍可复核"
                  >
                    复核已暂停
                  </Badge>
                )}
                <Button
                  size="sm"
                  variant={reviewsPaused ? 'soft' : 'outline'}
                  onClick={() => void toggleReviews()}
                  disabled={reviewBusy || reviews.loading}
                  title="复核开关独立于项目暂停：暂停只停复核，不恢复指挥官与执行者；项目暂停时仍可复核"
                >
                  {reviewsPaused ? <Play className="h-3.5 w-3.5" /> : <Pause className="h-3.5 w-3.5" />}
                  {reviewBusy ? '处理中…' : reviewsPaused ? '继续复核' : '暂停复核'}
                </Button>
              </>
            )}
            {canBatch && (
              <Button
                size="sm"
                variant="default"
                onClick={() => void verifyAll()}
                disabled={batchBusy || reviewsPaused}
                title={
                  reviewsPaused
                    ? '复核已暂停：点顶部「继续复核」后再排入'
                    : unverified > 0
                      ? `把本项目 ${unverified} 条未复核结论排入复核：一次真实的复核 run（消耗 token）`
                      : '把本项目所有未复核结论排入复核（真实模型运行）'
                }
              >
                <ShieldCheck className="h-3.5 w-3.5" />
                {batchBusy ? '排队中…' : '一键复核'}
              </Button>
            )}
            {locked && (
              <Button asChild size="sm" variant="outline">
                <a href={`/g/?view=vulns&project=${encodeURIComponent(lockedProject)}`}>
                  <ExternalLink className="h-3.5 w-3.5" /> 全局漏洞库
                </a>
              </Button>
            )}
          </div>
        ) : undefined
      }
    >
      {list.error && (
        <Callout tone="destructive" className="mb-3">
          {list.error}
          <Button size="xs" variant="outline" className="ml-2" onClick={list.reload}>
            重试
          </Button>
        </Callout>
      )}
      {batchNote && <Callout tone="info" className="mb-3">{batchNote}</Callout>}
      {reviewNote && <Callout tone="info" className="mb-3">{reviewNote}</Callout>}
      {verifyError && <Callout tone="destructive" className="mb-3">{verifyError}</Callout>}

      {/* 筛选工具条：12px、控件 h-8，放不下就换行，绝不横向滚动。 */}
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="relative w-full min-w-[180px] sm:w-[240px]">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" />
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="搜索标题 / 摘要"
            aria-label="搜索漏洞"
            className="h-8 w-full pl-7 text-[12px]"
          />
        </div>
        {!locked && (
          <Select
            value={project || ALL_PROJECTS}
            onValueChange={(v) => pickProject(v === ALL_PROJECTS ? '' : v)}
          >
            <SelectTrigger className="h-8 w-[170px] text-[12px]" aria-label="按项目过滤">
              <SelectValue placeholder="全部项目" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_PROJECTS}>全部项目</SelectItem>
              {projects.map((p) => (
                <SelectItem key={p.id} value={p.id}>
                  {p.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="按状态过滤">
          <Chip active={status === ''} onClick={() => setStatus('')}>
            全部
            <span className="tw-nums font-mono text-[11px] opacity-70">{rows.length}</span>
          </Chip>
          {STATUS_FILTERS.map((f) => (
            <Chip key={f.value} active={status === f.value} onClick={() => setStatus(f.value)}>
              {f.label}
              <span className="tw-nums font-mono text-[11px] opacity-70">{statusCount(f.value)}</span>
            </Chip>
          ))}
        </div>
        <span className="tw-nums ml-auto font-mono text-[11px] text-ink-3">
          显示 {visible.length} / {rows.length}
        </span>
      </div>

      <Panel
        title="漏洞条目"
        extra={
          <span className="tw-nums font-mono text-[11px] text-ink-3">
            共 {total} 条{matched !== total ? ` · 当前筛选 ${matched}` : ''}
          </span>
        }
        bodyClassName="p-0"
      >
        {list.loading && (
          <div className="p-4">
            <SkeletonList count={6} />
          </div>
        )}

        {!list.loading && rows.length === 0 && (
          <EmptyState
            icon={Bug}
            title={
              locked ? '本项目还没有入库漏洞' : project ? '这个项目没有入库漏洞' : '库里没有条目'
            }
            description={
              locked
                ? '执行者声明的 vuln 事实会在声明时镜像进全局漏洞库；这里的列表只是它的本项目切片，复核/驳回会更新状态。'
                : project
                  ? '换一个项目或清空筛选；删项目的条目仍在库里，只是标成 orphaned。'
                  : '执行者声明的 vuln 事实会镜像到这里；删项目不清空，只标 orphaned。'
            }
          />
        )}

        {!list.loading && rows.length > 0 && visible.length === 0 && (
          <EmptyState
            compact
            icon={Bug}
            title="没有符合状态筛选的条目"
            description="换一个状态，或清除筛选看全部条目。"
            action={
              <Button size="sm" variant="outline" onClick={() => setStatus('')}>
                清除筛选
              </Button>
            }
          />
        )}

        {visible.length > 0 && (
          <div className="divide-y divide-hairline">
            {visible.map((v) => {
              const active = v.id === selectedId
              return (
                <div
                  key={v.id}
                  onClick={() => openVuln(v)}
                  title="点击查看完整详情（摘要、复现、证据、复核结论）"
                  className={cn(
                    'group flex cursor-pointer items-start gap-3 px-2.5 py-2.5 transition-colors',
                    active
                      ? 'bg-primary/[0.09] shadow-[inset_2px_0_0_var(--primary)]'
                      : 'hover:bg-surface-2',
                  )}
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                      <button
                        type="button"
                        aria-expanded={active}
                        onClick={(e) => {
                          e.stopPropagation()
                          openVuln(v)
                        }}
                        className="tw-break max-w-full rounded text-left text-[13px] font-medium leading-5 text-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                      >
                        {v.title}
                      </button>
                      <StatusIndicator
                        compact
                        status={statusKind(v.status)}
                        label={statusLabel(v.status)}
                      />
                      {v.review?.verdict === 'needs_evidence' && <ReviewChip review={v.review} />}
                      {v.orphaned && (
                        <Badge size="sm" variant="warning">
                          orphaned
                        </Badge>
                      )}
                      {!locked && v.project && (
                        <Badge size="sm" variant="muted">
                          {v.project}
                        </Badge>
                      )}
                    </div>

                    {v.summary && (
                      // 列表只负责扫读：摘要压成两行，完整内容在详情面板里。
                      <div className="tw-break mt-1 line-clamp-2 text-[12px] leading-relaxed text-ink-2">
                        {v.summary}
                      </div>
                    )}
                    {v.review && v.review.verdict !== 'pass' && (
                      // 不打开面板也能看到「为什么没过」，不留下只有「复核未通过」一个词。
                      <div className="tw-break mt-1 text-[12px] leading-relaxed text-warning">
                        复核未通过：{oneLine(v.review.notes, 80) || statusLabel(v.review.verdict)}
                      </div>
                    )}
                    {(v.evidence ?? []).length > 0 && (
                      // 列表只露前三个证据：一行一条结论，全部证据和文件路径在详情面板里。
                      // 点击是预览（与「证据」页一致），不是把原始字节丢给新标签页或下载。
                      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1">
                        {v.evidence!.slice(0, 3).map((p) => (
                          <button
                            key={p}
                            type="button"
                            // 行本身可点开详情，证据按钮不能顺带触发它。
                            onClick={(e) => {
                              e.stopPropagation()
                              setEvidencePreview(vulnEvidencePreviewFile(p))
                            }}
                            title="预览证据（不离开漏洞页，不下载）"
                            className="flex min-w-0 items-center gap-1 rounded font-mono text-[11px] text-info hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                          >
                            <Eye className="h-3 w-3 shrink-0" />
                            <span className="truncate">{p.split('/').pop()}</span>
                          </button>
                        ))}
                        {v.evidence!.length > 3 && (
                          <span className="tw-nums text-[11px] text-ink-3">
                            还有 {v.evidence!.length - 3} 个（详情里看全部）
                          </span>
                        )}
                      </div>
                    )}
                    {(v.evidence_dropped ?? []).length > 0 && (
                      <div className="tw-nums mt-1 text-[11px] text-warning">
                        缺失证据 {v.evidence_dropped!.length} 个（项目删除时已不存在）
                      </div>
                    )}
                  </div>

                  <div className="flex shrink-0 flex-col items-end gap-1.5">
                    <span className="tw-nums whitespace-nowrap font-mono text-[11px] leading-4 text-ink-3">
                      {fmtTime(v.created_at)}
                    </span>
                    {!v.orphaned && v.status === 'unverified' && (
                      <Button
                        size="xs"
                        variant="outline"
                        disabled={verifyRows[v.id] !== undefined || reviewsPaused}
                        // 行本身可点开详情，行内动作不能顺带触发它。
                        onClick={(e) => {
                          e.stopPropagation()
                          void verifyRow(v)
                        }}
                        title={
                          reviewsPaused
                            ? '复核已暂停：点顶部「继续复核」后再复核；项目暂停不影响复核'
                            : '排一次复核 run：真实模型运行，通过后状态才会变成已复核'
                        }
                      >
                        <ShieldCheck className="h-3 w-3" />
                        {verifyRows[v.id] === 'queued'
                          ? '已排队'
                          : verifyRows[v.id] === 'busy'
                            ? '排队中…'
                            : '复核'}
                      </Button>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </Panel>

      <VulnSheet
        vuln={selected}
        verifyState={selected ? verifyRows[selected.id] : undefined}
        verifyError={verifyError}
        reviewsPaused={reviewsPaused}
        onOpenChange={(open) => {
          if (!open) closeVuln()
        }}
        onVerify={() => {
          if (selected) void verifyRow(selected)
        }}
        onPreviewEvidence={(path) => setEvidencePreview(vulnEvidencePreviewFile(path))}
      />
      {/* 预览叠在详情之上：关闭预览仍回到同一条漏洞，不读写 ?vuln=。 */}
      <EvidencePreview
        file={evidencePreview}
        onOpenChange={(open) => {
          if (!open) setEvidencePreview(null)
        }}
      />
    </Page>
  )
}

/**
 * 列表行里只给「复核过但没通过（缺证据）」补一个小标：pass 已由状态「已复核」
 * 表达，reject 已由「已否决」表达，再标一遍是同一个事实说两遍。verdict 与 status
 * 对不上的原因就在这里——结论状态仍是未复核，但复核 run 已经给过结论。
 */
function ReviewChip({ review }: { review: VulnReview }) {
  return (
    <Badge
      size="sm"
      variant="warning"
      title={oneLine(review.notes, 160) || statusLabel(review.verdict)}
    >
      复核未通过
    </Badge>
  )
}

/**
 * 一条漏洞的完整详情。面板数据全部来自列表响应本身（list endpoint 已带全量
 * doc/summary），打开不额外发请求，也就不会出现列表与详情两个版本。
 *
 * 阅读顺序：标题 + 状态 → 元信息（mono，两列）→ 复核结果 → 摘要 → 详情/复现
 * （MiniMarkdown）→ 证据（每行 mono + 预览 + 下载）→ 底部动作。分区之间用
 * hairline + 16px，分区内部 8–12px。
 */
function VulnSheet({
  vuln,
  verifyState,
  verifyError,
  reviewsPaused,
  onOpenChange,
  onVerify,
  onPreviewEvidence,
}: {
  vuln: VulnEntry | null
  verifyState?: 'busy' | 'queued'
  verifyError: string
  /** The review switch is independent of the project pause (D137). */
  reviewsPaused: boolean
  onOpenChange: (open: boolean) => void
  onVerify: () => void
  onPreviewEvidence: (path: string) => void
}) {
  const review = vuln?.review
  const evidence = vuln?.evidence ?? []
  const dropped = vuln?.evidence_dropped ?? []
  // 项目还在才给事实图链接：orphaned 条目背后没有项目页可打开。图里的节点 id
  // 带类型前缀（f:<事实 id>），hash 必须照它的选法写，否则打开后不会选中。
  const graphHref =
    vuln && vuln.project_id && !vuln.orphaned
      ? `/p/${encodeURIComponent(vuln.project_id)}/?view=graph#${encodeURIComponent(
          `f:${vuln.source_id}`,
        )}`
      : ''
  const verifyEligible =
    !!vuln && vuln.project_id !== '' && !vuln.orphaned && vuln.status === 'unverified'
  const showActions = verifyEligible || graphHref !== '' || verifyError !== ''

  return (
    <Sheet open={!!vuln} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 sm:max-w-3xl">
        {vuln && (
          <>
            <SheetHeader className="shrink-0 space-y-2 border-b border-hairline px-5 py-4 pr-12 text-left">
              <div className="tw-eyebrow">漏洞详情</div>
              <SheetTitle className="tw-break text-[17px] leading-6 text-ink">{vuln.title}</SheetTitle>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <StatusIndicator
                  compact
                  status={statusKind(vuln.status)}
                  label={statusLabel(vuln.status)}
                />
                {vuln.review?.verdict === 'needs_evidence' && <ReviewChip review={vuln.review} />}
                {vuln.orphaned && (
                  <Badge size="sm" variant="warning">
                    orphaned
                  </Badge>
                )}
                {vuln.project && (
                  <Badge size="sm" variant="muted">
                    {vuln.project}
                  </Badge>
                )}
              </div>
              <div className="tw-break flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px] leading-4 text-ink-3">
                <span>id {vuln.id}</span>
                <span className="tw-nums">入库 {fmtTime(vuln.created_at)}</span>
                <span className="tw-nums">更新 {fmtTime(vuln.updated_at)}</span>
              </div>
            </SheetHeader>

            {/* Radix 的 ScrollArea 用 table 布局撑宽水平滚动；这里只有纵向滚动，
                长路径/长命令行不该把内容顶成一条 15k 宽的窄条，按块级排版。 */}
            <ScrollArea className="min-h-0 flex-1 [&_[data-radix-scroll-area-viewport]>div]:!block">
              <div className="min-w-0 space-y-4 px-5 py-4">
                <section className="space-y-2">
                  <h3 className="tw-section-title">元信息</h3>
                  <dl className="grid grid-cols-1 gap-x-4 gap-y-2.5 rounded-lg border border-hairline bg-surface-2/40 p-3 sm:grid-cols-2">
                    <MetaItem label="来源事实 id" mono>
                      {vuln.source_id || '—'}
                    </MetaItem>
                    <MetaItem label="意图" mono={!vuln.intent} title={vuln.intent_id}>
                      {vuln.intent || vuln.intent_id || '—'}
                    </MetaItem>
                    <MetaItem label="置信度">{vuln.confidence || '—'}</MetaItem>
                    <MetaItem
                      label="证明方式"
                      title="dynamic＝有真实运行回显（动态验证）；static＝仅静态推断。旧数据缺该字段时按事实默认 dynamic 显示，与事实图一致。"
                    >
                      {`proof:${vuln.proof ?? 'dynamic'}`}
                    </MetaItem>
                  </dl>
                </section>

                <section
                  className={cn(
                    'space-y-2 rounded-lg border p-3',
                    review && review.verdict !== 'pass'
                      ? 'border-warning/35 bg-warning/[0.05]'
                      : 'border-hairline bg-surface-2/40',
                  )}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <h3 className="tw-section-title">复核结果</h3>
                    {review && (
                      <StatusIndicator
                        compact
                        status={statusKind(review.verdict)}
                        label={statusLabel(review.verdict)}
                      />
                    )}
                    {review && (
                      <span className="tw-nums ml-auto font-mono text-[11px] text-ink-3">
                        {fmtTime(review.created_at)}
                      </span>
                    )}
                  </div>
                  {review ? (
                    <>
                      {review.verifier_run && (
                        <div className="tw-break font-mono text-[11px] text-ink-3">
                          复核 run {review.verifier_run}
                        </div>
                      )}
                      {review.notes ? (
                        <MiniMarkdown text={review.notes} />
                      ) : (
                        <p className="text-[12px] leading-relaxed text-ink-2">
                          这次复核没有留下文字理由。
                        </p>
                      )}
                    </>
                  ) : (
                    <p className="tw-break text-[12px] leading-relaxed text-ink-2">
                      还没有复核结论。复核会排一次真实的 verifier run（消耗 token）；只有它能给出通过 / 缺证据 / 否决的判定和理由，人工按钮不会代替它。
                    </p>
                  )}
                </section>

                <section className="space-y-2 border-t border-hairline pt-4">
                  <h3 className="tw-section-title">摘要</h3>
                  {vuln.summary ? (
                    <p className="tw-break whitespace-pre-wrap text-[13px] leading-relaxed text-ink-2">
                      {vuln.summary}
                    </p>
                  ) : (
                    <p className="text-[12px] text-ink-3">这条结论没有摘要。</p>
                  )}
                </section>

                <section className="space-y-2 border-t border-hairline pt-4">
                  <h3 className="tw-section-title">详情 / 复现</h3>
                  {vuln.doc ? (
                    <div className="tw-inset rounded-lg p-3">
                      <MiniMarkdown text={vuln.doc} />
                    </div>
                  ) : (
                    <p className="text-[12px] leading-relaxed text-ink-3">
                      这条结论没有写入详情（doc 为空）——复现命令和请求/响应包都该在这里。
                    </p>
                  )}
                </section>

                <section className="space-y-2 border-t border-hairline pt-4">
                  <div className="flex items-center gap-2">
                    <h3 className="tw-section-title">证据</h3>
                    <span className="tw-nums font-mono text-[11px] text-ink-3">{evidence.length}</span>
                  </div>
                  {evidence.length === 0 && (
                    <p className="text-[12px] leading-relaxed text-ink-3">
                      没有证据副本；结论是否可信以复核结论为准。
                    </p>
                  )}
                  {evidence.length > 0 && (
                    <div className="space-y-1.5">
                      {evidence.map((p) => (
                        <div
                          key={p}
                          className="flex items-center gap-1.5 rounded-lg border border-hairline bg-surface-2/40 p-1 pl-2 transition-colors hover:border-ink-3/30"
                        >
                          <button
                            type="button"
                            onClick={() => onPreviewEvidence(p)}
                            title="预览证据（不离开漏洞页，不下载）"
                            className="tw-break flex min-w-0 flex-1 items-center gap-1.5 rounded py-1 text-left font-mono text-[11px] leading-4 text-info hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                          >
                            <Eye className="h-3.5 w-3.5 shrink-0" />
                            <span className="min-w-0 truncate">{p}</span>
                          </button>
                          <a
                            href={api.vulnEvidenceDownloadURL(p)}
                            download={p.split('/').pop() || undefined}
                            title="下载证据原件（显式保存）"
                            aria-label={`下载 ${p}`}
                            className="shrink-0 rounded-md p-1.5 text-ink-3 transition-colors hover:bg-surface-3 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                          >
                            <Download className="h-3.5 w-3.5" />
                          </a>
                        </div>
                      ))}
                    </div>
                  )}
                  {dropped.length > 0 && (
                    <Callout tone="warning">
                      <span className="tw-break">
                        缺失证据 {dropped.length} 个（项目删除时已不存在）：{dropped.join('、')}
                      </span>
                    </Callout>
                  )}
                </section>
              </div>
            </ScrollArea>

            {showActions && (
              <div className="shrink-0 space-y-2 border-t border-hairline px-5 py-3">
                <div className="flex flex-wrap items-center gap-2">
                  {verifyEligible && (
                    <Button
                      size="sm"
                      variant="soft"
                      disabled={verifyState !== undefined || reviewsPaused}
                      onClick={onVerify}
                      title={
                        reviewsPaused
                          ? '复核已暂停：点顶部「继续复核」后再复核；项目暂停不影响复核'
                          : '排一次复核 run：真实模型运行，通过后状态才会变成已复核'
                      }
                    >
                      <ShieldCheck className="h-3.5 w-3.5" />
                      {verifyState === 'queued'
                        ? '已排队'
                        : verifyState === 'busy'
                          ? '排队中…'
                          : reviewsPaused
                            ? '复核已暂停'
                            : '复核'}
                    </Button>
                  )}
                  {graphHref !== '' && (
                    <Button asChild size="sm" variant="outline">
                      <a href={graphHref} title="在项目事实意图图中定位来源事实">
                        <Waypoints className="h-3.5 w-3.5" /> 在事实图中查看
                      </a>
                    </Button>
                  )}
                </div>
                {verifyEligible && (
                  <p className="text-[11px] leading-relaxed text-ink-3">
                    {reviewsPaused
                      ? '复核开关是关的：项目暂停不影响复核，但复核本身需要点顶部「继续复核」。'
                      : '复核是真实模型 run（消耗 token）：跑完后结论、理由和复核 run id 会出现在上面的「复核结果」里。'}
                  </p>
                )}
                {verifyError && <Callout tone="destructive">{verifyError}</Callout>}
              </div>
            )}
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}

/** 详情面板里的一格元信息（dl > div > dt/dd，HTML5 允许这种包法）。 */
function MetaItem({
  label,
  mono,
  title,
  children,
}: {
  label: string
  mono?: boolean
  title?: string
  children: ReactNode
}) {
  return (
    <div className="min-w-0" title={title}>
      <dt className="text-[11px] font-medium leading-4 text-ink-3">{label}</dt>
      <dd
        className={cn(
          'tw-break mt-1 text-[12px] leading-5 text-ink-2',
          mono && 'font-mono text-[11px]',
        )}
      >
        {children}
      </dd>
    </div>
  )
}
