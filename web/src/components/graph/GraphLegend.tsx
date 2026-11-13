// 图例：把画布上的颜色/线型翻译成人话。
//
// 它是参考卡，不是仪表盘：默认折叠。第一次进入图谱页时出现一次提示
// （localStorage 键 `tw.graph.legendHint`），点开图例或关掉提示后不再出现。
// 文案必须与 render.ts 的绘制一致：颜色、线型、虚线环都是承诺，不是装饰。
import { useEffect, useState } from 'react'

/** 只提示一次：操作者看过图例后，角落不再重复解释。 */
const LEGEND_HINT_KEY = 'tw.graph.legendHint'

const FACT_KINDS: readonly [string, string][] = [
  ['vuln', '#ff4d4d'],
  ['cred', '#b388ff'],
  ['host', '#6aa9ff'],
  ['service', '#2dd4bf'],
  ['loot', '#ffc457'],
  ['artifact', '#ff9f43'],
  ['note', '#8b8b93'],
]

const INTENT_STATUSES: readonly [string, string][] = [
  ['排队', '#f2c94c'],
  ['执行中', '#ff4d4d'],
  ['失败', '#e879f9'],
  ['完成', '#39d98a'],
  ['停放', '#7b8494'],
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

function Row({ children, title }: { children: React.ReactNode; title?: string }) {
  return (
    <span className="flex items-center gap-1.5 text-muted-foreground" title={title}>
      {children}
    </span>
  )
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
      className={`pointer-events-auto w-[260px] rounded-lg border border-border bg-card/85 text-[11px] shadow-lifted backdrop-blur ${className ?? ''}`}
      data-testid="graph-legend"
    >
      <button
        type="button"
        onClick={() => (open ? setOpen(false) : openLegend())}
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-muted-foreground hover:text-foreground"
      >
        <span className={`inline-block text-[10px] transition-transform ${open ? 'rotate-0' : '-rotate-90'}`} aria-hidden="true">
          ▼
        </span>
        图例
        {!open && <span className="ml-auto text-[10px] opacity-60">点开</span>}
      </button>

      {hint && !open && (
        <div className="mx-2 mb-1.5 flex items-start gap-1.5 rounded border border-info/30 bg-info/10 px-2 py-1.5 text-[10px] leading-relaxed text-muted-foreground">
          <span className="flex-1">图例说明节点的颜色、关系的线型与焦点叠层；点「图例」展开。</span>
          <button
            type="button"
            onClick={dismissHint}
            className="shrink-0 rounded px-1 text-muted-foreground hover:bg-muted/60 hover:text-foreground"
            title="不再提示"
          >
            知道了
          </button>
        </div>
      )}

      {open && (
        <div className="border-t border-border/60 px-2.5 py-2">
          <div className="mb-1 uppercase tracking-wider text-muted-foreground">事实（节点）</div>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            {FACT_KINDS.map(([name, color]) => (
              <Row key={name}>
                <Dot color={color} />
                {name}
              </Row>
            ))}
            <Row title="项目简报是图的起点">
              <span className="h-2 w-3.5 shrink-0 rounded-sm border border-[#8b8b93]" aria-hidden="true" />
              项目/范围
            </Row>
            <Row title="目标不单独上图：达成后作为终点接到承载证据的事实上；未达成时只在左下「终点」卡显示">
              <span className="h-2 w-3.5 shrink-0 rounded-sm border border-[#39d98a]" aria-hidden="true" />
              目标（达成后）
            </Row>
          </div>

          <div className="mb-1 mt-2 uppercase tracking-wider text-muted-foreground">意图（边）</div>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            {INTENT_STATUSES.map(([name, color]) => (
              <Row key={name}>
                <span className="h-2 w-3.5 shrink-0 rounded-sm border" style={{ borderColor: color }} aria-hidden="true" />
                {name}
              </Row>
            ))}
          </div>

          <div className="mb-1 mt-2 uppercase tracking-wider text-muted-foreground">关系（边）</div>
          <div className="space-y-0.5">
            <Row title="意图边（步骤 → 产出的每个事实）：颜色跟随方向状态；它表示这次工作产出了什么，不是前提">
              <Line color="rgba(154,160,170,0.85)" style="solid" />
              产出（步骤 → 事实）
            </Row>
            <Row title="from_fact_ids：方向派发的硬门槛；蓝色实线是唯一「硬门槛」色，前提缺失或被否决会挡住派发">
              <Line color="rgba(106,169,255,0.9)" style="solid" />
              必要前提
            </Row>
            <Row title="context_fact_ids：阅读选择，不是派发门槛；青色虚线">
              <Line color="rgba(45,212,191,0.6)" style="dashed" />
              引用（青色虚线）
            </Row>
            <Row title="pinned_fact_ids：明确固定要读的上下文，青色实线双线；仍是阅读选择，不是门槛">
              <Line color="rgba(45,212,191,0.95)" style="double" />
              固定（青色实线双线）
            </Row>
            <Row title="事实支持已达成目标所依据的证据（账本 supports 边）；金色细点线">
              <Line color="rgba(255,209,102,0.55)" style="dotted" />
              支持目标
            </Row>
            <Row title="旧卡片的盖章只记录了证据 id 时，由承载该证据的事实连到目标；朴素金色实线，与上面的依据边区分">
              <Line color="rgba(255,209,102,0.4)" style="solid" />
              目标证据（旧盖章）
            </Row>
            <Row title="parent_intent_id：父意图委派出的子意图；紫色虚线">
              <Line color="rgba(179,136,255,0.75)" style="dashed" />
              子任务委派（非模型等级）
            </Row>
            <Row title="项目简报到初始方向：图的起点，不是事实关系；灰色细线">
              <Line color="rgba(139,139,147,0.5)" style="solid" />
              项目范围
            </Row>
            <Row title="service 事实落到同名 host 事实上的分组线（宿主机归属）；灰色点线，不算因果">
              <Line color="rgba(139,139,147,0.35)" style="dotted" />
              宿主机分组
            </Row>
          </div>
          <div className="mt-1 text-[10px] leading-relaxed text-muted-foreground">
            引用/固定是阅读选择，不是必须满足的前提；缺前提挡派发，缺引用不挡。引用、同实体与
            宿主机分组只表示「读到/属于」，不会被画成因果链。
          </div>

          <div className="mb-1 mt-2 uppercase tracking-wider text-muted-foreground">标记</div>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            <Row title="事实的 claim_version 高于最新一次复核的版本：琥珀色虚线外环表示现在复核的是旧断言">
              <span
                className="h-3 w-3 shrink-0 rounded-full border border-dashed"
                style={{ borderColor: 'rgba(245,158,11,0.9)' }}
                aria-hidden="true"
              />
              复核的是旧断言
            </Row>
            <Row title="该意图的 run 记录了非致命告警，悬停看详情">
              <span
                className="flex h-3 w-3 shrink-0 items-center justify-center rounded-full bg-[#f59e0b] text-[8px] font-bold text-black"
                aria-hidden="true"
              >
                !
              </span>
              警告数
            </Row>
            <Row title="画布方向键选中的节点：天蓝色环，可与选中/悬停同时出现">
              <span
                className="h-3 w-3 shrink-0 rounded-full border-2"
                style={{ borderColor: 'rgba(56,189,248,0.95)' }}
                aria-hidden="true"
              />
              键盘焦点
            </Row>
            <Row title="引用边界：引用指向的记录不在当前快照（账本里仍然存在）。这不是孤立事实，也不是缺失；点开加载它的邻域">
              <span
                className="h-3 w-3 shrink-0 rounded-full border border-dashed border-[#9aa0aa]"
                aria-hidden="true"
              />
              未载入引用（可展开）
            </Row>
            <Row title="引用指向的 id 在账本里也不存在：点开不会变出内容">
              <span
                className="h-3 w-3 shrink-0 rounded-full border border-dashed"
                style={{ borderColor: 'rgba(255,77,77,0.72)' }}
                aria-hidden="true"
              />
              引用缺失
            </Row>
            <Row title="引用的事实被明确否定：仍然存在于账本，但不应作为当前依据">
              <span
                className="h-3 w-3 shrink-0 rounded-full border border-dashed"
                style={{ borderColor: 'rgba(232,121,249,0.72)' }}
                aria-hidden="true"
              />
              已否定引用
            </Row>
          </div>
          <div className="mb-1 mt-2 uppercase tracking-wider text-muted-foreground">焦点叠层</div>
          <div className="space-y-0.5">
            <Row title="选中意图后，焦点投影里的 hot 事实保持全亮，并在节点外加一圈白色环">
              <span
                className="h-3 w-3 shrink-0 rounded-full border-2 border-white bg-[#ff4d4d] shadow-[0_0_0_1px_rgba(0,0,0,0.45)]"
                aria-hidden="true"
              />
              热：全亮 + 白环
            </Row>
            <Row title="warm 事实保持正常亮度，不加环">
              <span className="h-3 w-3 shrink-0 rounded-full bg-[#f2c94c]" aria-hidden="true" />
              温：正常亮度
            </Row>
            <Row title="cold 与不在焦点层内的事实淡化到 0.18；与选中淡化取更暗者，不会更亮；只影响绘制，不删节点">
              <span className="h-3 w-3 shrink-0 rounded-full bg-[#6b7280] opacity-30" aria-hidden="true" />
              冷/层外：淡化到 0.18
            </Row>
          </div>

          <div className="mt-1.5 text-[10px] leading-relaxed text-muted-foreground">
            方块第二行 = 实际模型；力导向视图的虚线环 = 档位。
          </div>
          <div className="mt-1 text-[10px] leading-relaxed text-muted-foreground">
            悬停看详情 · 滚轮缩放 · 拖拽节点 · 双击适配
          </div>
        </div>
      )}
    </div>
  )
}
