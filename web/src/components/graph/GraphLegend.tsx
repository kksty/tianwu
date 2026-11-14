// 图例：把画布上的颜色/线型翻译成人话。
//
// 它是参考卡，不是仪表盘：默认折叠。第一次进入图谱页时出现一次提示
// （localStorage 键 `tw.graph.legendHint`），点开图例或关掉提示后不再出现。
// 文案必须与 render.ts 的绘制一致：颜色、线型、虚线环都是承诺，不是装饰。
// 因此这里不重新声明色值，而是直接 import render.ts 的绘制色表——改画笔时
// 图例自动跟着改，不会再出现「图例说青色、画布画绿色」的漂移。
import { useEffect, useState } from 'react'
import { ChevronDown } from 'lucide-react'
import {
  BOUNDARY_COLORS,
  CANVAS,
  EDGE_COLORS,
  FACT_COLORS,
  GOAL_COLORS,
  INTENT_COLORS,
  KEYBOARD_FOCUS_RING,
  PROJECT_COLOR,
  REVIEW_RING,
} from '@/graph/render'

/** 只提示一次：操作者看过图例后，角落不再重复解释。 */
const LEGEND_HINT_KEY = 'tw.graph.legendHint'

const FACT_KINDS = ['vuln', 'cred', 'host', 'service', 'loot', 'artifact', 'note'] as const

/** 中文状态名 → render.ts 的意图状态色键。 */
const INTENT_STATUSES: readonly [string, string][] = [
  ['排队', 'pending'],
  ['执行中', 'running'],
  ['固定复核中', 'reviewing'],
  ['失败', 'failed'],
  ['复核未通过', 'review_failed'],
  ['完成', 'done'],
  ['停放', 'parked'],
]

function readHintSeen(): boolean {
  if (typeof window === 'undefined') return true
  try {
    return window.localStorage.getItem(LEGEND_HINT_KEY) === '1'
  } catch {
    // 隐私模式/配额异常时按“已看过”处理，绝不因存储失败反复打扰。
    return true
  }
}

function markHintSeen(): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(LEGEND_HINT_KEY, '1')
  } catch {
    /* 存不下就算了，提示只影响本次会话 */
  }
}

function Dot({ color }: { color: string }) {
  return <span className="h-2 w-2 shrink-0 rounded-full" style={{ background: color }} aria-hidden="true" />
}

function Line({ color, style }: { color: string; style: 'solid' | 'dashed' | 'dotted' | 'double' }) {
  if (style === 'double') {
    return (
      <span className="flex h-3 w-4 shrink-0 flex-col justify-center gap-[2px]" aria-hidden="true">
        <span className="h-0.5 w-4 rounded-full" style={{ background: color }} />
        <span className="h-0.5 w-4 rounded-full" style={{ background: color }} />
      </span>
    )
  }
  return (
    <span
      className="h-0 w-4 shrink-0"
      style={{
        borderTopWidth: 2,
        borderTopStyle: style === 'dotted' ? 'dotted' : style === 'dashed' ? 'dashed' : 'solid',
        borderColor: color,
      }}
      aria-hidden="true"
    />
  )
}

function Ring({ color, dashed = false, width = 1 }: { color: string; dashed?: boolean; width?: number }) {
  return (
    <span
      className="h-3 w-3 shrink-0 rounded-full"
      style={{ border: `${width}px ${dashed ? 'dashed' : 'solid'} ${color}` }}
      aria-hidden="true"
    />
  )
}

/** A legend row reads as a quiet reference line, but still responds to hover. */
function Row({ children, title }: { children: React.ReactNode; title?: string }) {
  return (
    <span
      className="-mx-1 flex items-center gap-1.5 rounded px-1 py-0.5 text-ink-2 transition-colors hover:bg-surface-2 hover:text-ink"
      title={title}
    >
      {children}
    </span>
  )
}

function GroupTitle({ children }: { children: React.ReactNode }) {
  return <div className="tw-eyebrow mb-1 mt-2 first:mt-0">{children}</div>
}

function Hint({ children }: { children: React.ReactNode }) {
  return <div className="mt-1.5 text-[11px] leading-relaxed text-ink-3">{children}</div>
}

export function GraphLegend({ className }: { className?: string }) {
  // 折叠默认；第一次进入给一次提示。
  const [open, setOpen] = useState(false)
  const [hint, setHint] = useState(() => !readHintSeen())

  // 提示只在第一次进入时出现：一显示就记下，用户这次没点开也不会下次再来烦。
  useEffect(() => {
    if (hint) markHintSeen()
  }, [hint])

  const dismissHint = () => {
    setHint(false)
    markHintSeen()
  }
  const openLegend = () => {
    setOpen(true)
    if (hint) dismissHint()
    else markHintSeen()
  }

  return (
    <div
      className={`pointer-events-auto w-[292px] overflow-hidden rounded-xl border border-hairline bg-card/95 text-[11px] shadow-lifted backdrop-blur ${className ?? ''}`}
      data-testid="graph-legend"
    >
      <button
        type="button"
        onClick={() => (open ? setOpen(false) : openLegend())}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-left transition-colors hover:bg-surface-2"
      >
        <ChevronDown
          className={`h-4 w-4 shrink-0 text-ink-3 transition-transform duration-150 ${open ? '' : '-rotate-90'}`}
          aria-hidden="true"
        />
        <span className="tw-section-title">图例</span>
        {!open && <span className="ml-auto text-[11px] font-normal text-ink-3">点开</span>}
      </button>

      {hint && !open && (
        <div className="mx-2.5 mb-2 flex items-start gap-2 rounded-lg border border-info/25 bg-info/[0.07] px-2.5 py-2 text-[11px] leading-relaxed text-ink-2">
          <span className="flex-1">图例说明节点的颜色、关系的线型与焦点叠层；点「图例」展开。</span>
          <button
            type="button"
            onClick={dismissHint}
            className="shrink-0 rounded px-1 text-ink-3 transition-colors hover:bg-surface-2 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
            title="不再提示"
          >
            知道了
          </button>
        </div>
      )}

      {open && (
        <div className="max-h-[calc(100vh-7rem)] overflow-y-auto border-t border-hairline px-3 py-2.5">
          <GroupTitle>事实（节点）</GroupTitle>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            {FACT_KINDS.map((name) => (
              <Row key={name}>
                <Dot color={FACT_COLORS[name] ?? CANVAS.ink3} />
                {name}
              </Row>
            ))}
            <Row title="项目简报是图的起点">
              <span
                className="h-2 w-3.5 shrink-0 rounded-sm border"
                style={{ borderColor: PROJECT_COLOR }}
                aria-hidden="true"
              />
              项目/范围
            </Row>
            <Row title="目标不单独上图：达成后作为终点接到承载证据的事实上；未达成时只在左下「终点」卡显示">
              <span
                className="h-2 w-3.5 shrink-0 rounded-sm border"
                style={{ borderColor: GOAL_COLORS.met }}
                aria-hidden="true"
              />
              目标（达成后）
            </Row>
          </div>

          <GroupTitle>意图（边）</GroupTitle>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            {INTENT_STATUSES.map(([name, key]) => (
              <Row key={key}>
                <span
                  className="h-2 w-3.5 shrink-0 rounded-sm border"
                  style={{ borderColor: INTENT_COLORS[key] ?? CANVAS.ink3 }}
                  aria-hidden="true"
                />
                {name}
              </Row>
            ))}
          </div>
          <Hint>
            固定复核中（reviewing）的产出边画成虚线蓝：蓝色实线只表示「必要前提」这一种硬门槛。
          </Hint>

          <GroupTitle>关系（边）</GroupTitle>
          <div className="space-y-0.5">
            <Row title="意图边（步骤 → 产出的每个事实）：颜色跟随方向状态；它表示这次工作产出了什么，不是前提">
              <Line color="rgba(169,178,193,0.85)" style="solid" />
              产出（步骤 → 事实）
            </Row>
            <Row title="from_fact_ids：方向派发的硬门槛；蓝色实线是唯一「硬门槛」色。前提不可派发时会改成虚线红（阻挡）">
              <Line color={EDGE_COLORS.premise} style="solid" />
              必要前提
            </Row>
            <Row title="不可复核/未核验/缺失的前提都会挡住派发（与准入同源规则）；虚线红，复核通过后恢复蓝色实线">
              <Line color={EDGE_COLORS.premiseBlocked} style="dashed" />
              前提不满足（阻挡）
            </Row>
            <Row title="context_fact_ids：阅读选择，不是派发门槛；绿色虚线">
              <Line color={EDGE_COLORS.reference} style="dashed" />
              引用（绿色虚线）
            </Row>
            <Row title="pinned_fact_ids：明确固定要读的上下文，绿色实线双线；仍是阅读选择，不是门槛">
              <Line color={EDGE_COLORS.pin} style="double" />
              固定（绿色实线双线）
            </Row>
            <Row title="事实支持已达成目标所依据的证据（账本 supports 边）；金色细点线">
              <Line color={EDGE_COLORS.supports} style="dotted" />
              支持目标
            </Row>
            <Row title="旧卡片的盖章只记录了证据 id 时，由承载该证据的事实连到目标；朴素金色实线，与上面的依据边区分">
              <Line color={EDGE_COLORS.goal} style="solid" />
              目标证据（旧盖章）
            </Row>
            <Row title="parent_intent_id：父意图委派出的子意图；紫色虚线">
              <Line color={EDGE_COLORS.spawn} style="dashed" />
              子任务委派（非模型等级）
            </Row>
            <Row title="项目简报到初始方向：图的起点，不是事实关系；灰色细线">
              <Line color={EDGE_COLORS.brief} style="solid" />
              项目范围
            </Row>
            <Row title="service 事实落到同名 host 事实上的分组线（宿主机归属）；灰色点线，不算因果">
              <Line color={EDGE_COLORS.host} style="dotted" />
              宿主机分组
            </Row>
          </div>
          <Hint>
            引用/固定是阅读选择，不是必须满足的前提；缺前提挡派发，缺引用不挡。引用、同实体与
            宿主机分组只表示「读到/属于」，不会被画成因果链。
          </Hint>

          <GroupTitle>标记</GroupTitle>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            <Row title="事实的 claim_version 高于最新一次复核的版本：琥珀色虚线外环表示现在复核的是旧断言">
              <Ring color={REVIEW_RING} dashed />
              复核的是旧断言
            </Row>
            <Row title="该意图的 run 记录了非致命告警，悬停看详情">
              <span
                className="flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-full text-[11px] font-bold"
                style={{ background: CANVAS.warning, color: CANVAS.background }}
                aria-hidden="true"
              >
                !
              </span>
              警告数
            </Row>
            <Row title="画布方向键选中的节点：天蓝色环，可与选中/悬停同时出现">
              <Ring color={KEYBOARD_FOCUS_RING} width={2} />
              键盘焦点
            </Row>
            <Row title="引用边界：引用指向的记录不在当前快照（账本里仍然存在）。这不是孤立事实，也不是缺失；点开加载它的邻域">
              <Ring color={BOUNDARY_COLORS.not_loaded} dashed />
              未载入引用（可展开）
            </Row>
            <Row title="引用指向的 id 在账本里也不存在：点开不会变出内容">
              <Ring color={BOUNDARY_COLORS.missing} dashed />
              引用缺失
            </Row>
            <Row title="引用的事实被明确否定：仍然存在于账本，但不应作为当前依据">
              <Ring color={BOUNDARY_COLORS.rejected} dashed />
              已否定引用
            </Row>
          </div>
          <GroupTitle>焦点叠层</GroupTitle>
          <div className="space-y-0.5">
            <Row title="选中意图后，焦点投影里的 hot 事实保持全亮，并在节点外加一圈白色环">
              <span className="relative flex h-3 w-3 shrink-0" aria-hidden="true">
                <span
                  className="absolute inset-0 rounded-full"
                  style={{ background: FACT_COLORS.vuln, boxShadow: `0 0 0 1px ${CANVAS.ink}` }}
                />
              </span>
              热：全亮 + 白环
            </Row>
            <Row title="warm 事实保持正常亮度，不加环">
              <span
                className="h-3 w-3 shrink-0 rounded-full"
                style={{ background: INTENT_COLORS.pending }}
                aria-hidden="true"
              />
              温：正常亮度
            </Row>
            <Row title="cold 与不在焦点层内的事实淡化到 0.18；与选中淡化取更暗者，不会更亮；只影响绘制，不删节点">
              <span
                className="h-3 w-3 shrink-0 rounded-full opacity-30"
                style={{ background: CANVAS.ink3 }}
                aria-hidden="true"
              />
              冷/层外：淡化到 0.18
            </Row>
          </div>

          <Hint>方块第二行 = 实际模型；力导向视图的虚线环 = 档位。</Hint>
          <Hint>悬停看详情 · 滚轮缩放 · 拖拽节点 · 右侧面板左缘可拖动调宽</Hint>
        </div>
      )}
    </div>
  )
}
