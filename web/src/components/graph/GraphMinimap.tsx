// 缩略图：整张图的极简俯视图 + 当前视口矩形。
//
// 画布把相机发布到 view-store（docs/33 §4.5）；这里用 useView 订阅，所以只有
// 订阅者随相机变化重渲染，图谱根组件不因平移/缩放而重渲染。点击或拖动把
// 屏幕坐标换算回世界坐标交给 onCenter（GraphPanel 接 canvas.centerOn）。
import { useEffect, useMemo, useRef } from 'react'
import type { PointerEvent as ReactPointerEvent } from 'react'
import type { GraphModel, GraphNode } from '@/graph/model'
import { nodeColor } from '@/graph/render'
import { useView } from '@/graph/view-store'

/** CSS 尺寸固定：缩略图是参考物，不参与页面布局博弈。 */
const WIDTH = 168
const HEIGHT = 112
const PADDING = 6

interface WorldBounds {
  minX: number
  minY: number
  maxX: number
  maxY: number
}

interface MinimapTransform {
  scale: number
  offsetX: number
  offsetY: number
  toWorldX: (px: number) => number
  toWorldY: (py: number) => number
}

/** 节点在图坐标里的半尺寸；用绘制形状而非标签宽度，缩略图只需要大致轮廓。 */
function nodeHalf(n: GraphNode): { halfW: number; halfH: number } {
  if (n.type === 'fact' || n.type === 'ghost') return { halfW: 5, halfH: 5 }
  if (n.type === 'step') return { halfW: 8, halfH: 8 }
  return { halfW: Math.max(n.w, 8), halfH: Math.max(n.h, 8) }
}

function worldBounds(model: GraphModel): WorldBounds | null {
  if (model.nodes.length === 0) return null
  let minX = Infinity
  let minY = Infinity
  let maxX = -Infinity
  let maxY = -Infinity
  for (const n of model.nodes) {
    const { halfW, halfH } = nodeHalf(n)
    minX = Math.min(minX, n.x - halfW)
    minY = Math.min(minY, n.y - halfH)
    maxX = Math.max(maxX, n.x + halfW)
    maxY = Math.max(maxY, n.y + halfH)
  }
  // A single node (or a stack at the same point) still needs a non-zero span.
  if (!(maxX > minX)) {
    minX -= 10
    maxX += 10
  }
  if (!(maxY > minY)) {
    minY -= 10
    maxY += 10
  }
  return { minX, minY, maxX, maxY }
}

/** 等比缩放并居中，世界坐标 → 缩略图像素；反向换算用于点击定位。 */
function minimapTransform(bounds: WorldBounds, width: number, height: number, pad: number): MinimapTransform {
  const spanX = bounds.maxX - bounds.minX
  const spanY = bounds.maxY - bounds.minY
  const innerW = Math.max(width - pad * 2, 1)
  const innerH = Math.max(height - pad * 2, 1)
  const scale = Math.min(innerW / spanX, innerH / spanY)
  const offsetX = pad + (innerW - spanX * scale) / 2
  const offsetY = pad + (innerH - spanY * scale) / 2
  return {
    scale,
    offsetX,
    offsetY,
    toWorldX: (px) => bounds.minX + (px - offsetX) / scale,
    toWorldY: (py) => bounds.minY + (py - offsetY) / scale,
  }
}

function clampToBounds(value: number, min: number, max: number): number {
  if (value < min) return min
  if (value > max) return max
  return value
}

export function GraphMinimap({
  model,
  onCenter,
}: {
  model: GraphModel
  onCenter: (worldX: number, worldY: number) => void
}) {
  const view = useView()
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const dragging = useRef(false)
  const bounds = useMemo(() => worldBounds(model), [model])

  // 画布：节点缩略 + 视口矩形。只在相机/模型变化时重画。
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas || !bounds) return
    const dpr = typeof window !== 'undefined' && window.devicePixelRatio > 0 ? window.devicePixelRatio : 1
    const pixelW = Math.round(WIDTH * dpr)
    const pixelH = Math.round(HEIGHT * dpr)
    if (canvas.width !== pixelW) canvas.width = pixelW
    if (canvas.height !== pixelH) canvas.height = pixelH
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, WIDTH, HEIGHT)
    const map = minimapTransform(bounds, WIDTH, HEIGHT, PADDING)

    for (const n of model.nodes) {
      const px = map.offsetX + (n.x - bounds.minX) * map.scale
      const py = map.offsetY + (n.y - bounds.minY) * map.scale
      ctx.globalAlpha = n.type === 'project' ? 0.95 : 0.68
      ctx.fillStyle = nodeColor(n)
      ctx.fillRect(px - 1, py - 1, 2.2, 2.2)
    }
    ctx.globalAlpha = 1

    // 视口矩形：相机把世界坐标映射为 screen = world * k + cam。
    const cam = view.camera
    if (view.width > 0 && view.height > 0 && cam.k > 0) {
      const x0 = (0 - cam.x) / cam.k
      const y0 = (0 - cam.y) / cam.k
      const x1 = (view.width - cam.x) / cam.k
      const y1 = (view.height - cam.y) / cam.k
      const rx = map.offsetX + (x0 - bounds.minX) * map.scale
      const ry = map.offsetY + (y0 - bounds.minY) * map.scale
      const rw = (x1 - x0) * map.scale
      const rh = (y1 - y0) * map.scale
      ctx.lineWidth = 1
      // ink-1 at 85%: the viewport rectangle is chrome, not a semantic colour.
      ctx.strokeStyle = 'rgba(231,234,240,0.85)'
      ctx.strokeRect(rx + 0.5, ry + 0.5, Math.max(rw, 2), Math.max(rh, 2))
    }
  }, [bounds, model, view])

  if (!bounds || view.width <= 0 || view.height <= 0) return null

  const point = (event: ReactPointerEvent<HTMLCanvasElement>) => {
    const map = minimapTransform(bounds, WIDTH, HEIGHT, PADDING)
    const rect = event.currentTarget.getBoundingClientRect()
    const px = ((event.clientX - rect.left) * WIDTH) / Math.max(rect.width, 1)
    const py = ((event.clientY - rect.top) * HEIGHT) / Math.max(rect.height, 1)
    onCenter(
      clampToBounds(map.toWorldX(px), bounds.minX, bounds.maxX),
      clampToBounds(map.toWorldY(py), bounds.minY, bounds.maxY),
    )
  }

  return (
    <div
      className="pointer-events-auto overflow-hidden rounded-xl border border-hairline bg-card/90 p-1 shadow-lifted backdrop-blur"
      title="缩略图：点击或拖动定位到图上的位置"
    >
      <canvas
        ref={canvasRef}
        data-testid="graph-minimap"
        aria-label="图谱缩略图，点击或拖动可定位视图"
        className="block h-[112px] w-[168px] cursor-crosshair rounded-md"
        style={{ width: WIDTH, height: HEIGHT }}
        onPointerDown={(event) => {
          event.preventDefault()
          dragging.current = true
          try {
            event.currentTarget.setPointerCapture(event.pointerId)
          } catch {
            /* 老浏览器不支持捕获也不影响单击定位 */
          }
          point(event)
        }}
        onPointerMove={(event) => {
          if (!dragging.current) return
          event.preventDefault()
          point(event)
        }}
        onPointerUp={(event) => {
          dragging.current = false
          try {
            event.currentTarget.releasePointerCapture(event.pointerId)
          } catch {
            /* 已释放 */
          }
        }}
        onPointerCancel={() => {
          dragging.current = false
        }}
      />
    </div>
  )
}
