// 焦点叠层（P1-1）：选中一个意图后，取服务端的 focus 投影首页，把每条事实的
// temperature 映射成 canvas 认的节点 id（f:<fact id>）。
//
// 这里只读服务端的分类，不推断：首页之外的事实不进层（不在层内即淡化），
// 绝不把没返回的事实猜成 cold。关闭、无选中、请求出错都返回 null（=关闭叠层）。
import { useEffect, useState } from 'react'
import { api } from '@/lib/api'
import type { FocusTemperature, FocusTemperatures } from '@/graph/render'

/** 第一页即可覆盖一次派发的有界焦点索引；再多会让叠层变成整图快照。 */
const FOCUS_PAGE_LIMIT = 100

function isTemperature(value: string): value is FocusTemperature {
  return value === 'hot' || value === 'warm' || value === 'cold'
}

export function useFocusLayer(intentID: string | null, enabled: boolean): FocusTemperatures | null {
  const [layer, setLayer] = useState<FocusTemperatures | null>(null)

  useEffect(() => {
    if (!enabled || !intentID) {
      setLayer(null)
      return
    }
    let cancelled = false
    // 换意图时先清空：旧意图的层不能短暂盖在新选中的链路上。
    setLayer(null)
    api
      .focus({ intent: intentID, layer: 'focus', limit: FOCUS_PAGE_LIMIT })
      .then((page) => {
        if (cancelled) return
        const facts = page.facts ?? []
        if (facts.length === 0) {
          // 空投影不是“全部冷却”：让整图暗到 0.18 等于没图，按关闭处理。
          setLayer(null)
          return
        }
        const next = new Map<string, FocusTemperature>()
        for (const fact of facts) {
          if (fact?.id && isTemperature(fact.temperature)) next.set(`f:${fact.id}`, fact.temperature)
        }
        setLayer(next.size > 0 ? next : null)
      })
      .catch(() => {
        if (!cancelled) setLayer(null)
      })
    return () => {
      cancelled = true
    }
  }, [intentID, enabled])

  return layer
}
