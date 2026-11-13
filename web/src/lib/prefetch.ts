/**
 * Chunk prefetching for the console's lazy panels.
 *
 * React.lazy starts downloading a panel's chunk at the moment the panel is first
 * rendered, so a click on the rail shows a fallback first. The rail can start
 * that download on hover instead; this registry makes the intent side stay
 * idempotent (hover, focus and a fast click may all fire) and keeps a failed
 * preload from poisoning the lazy import that will run for real later.
 *
 * The registry is deliberately unaware of React and of the bundler: callers
 * register `() => import('...')` loaders and ask for them by id.
 */
export interface PrefetchRegistry {
  /** 登记一个 id 的 loader；重复登记覆盖 loader，不重放已发起的请求。 */
  register(id: string, load: () => Promise<unknown>): void
  /** 发起一次预取；加载中/已成功/未注册的 id 都安全 no-op，失败吞掉。 */
  prefetch(id: string): void
  /** 是否发起过预取（测试用）。 */
  isStarted(id: string): boolean
}

interface PrefetchEntry {
  load: () => Promise<unknown>
  /** 发起过预取。失败后仍为 true：这里回答"有没有发起"，不是"成没成功"。 */
  started: boolean
  /** 在途请求；失败后清空，于是下一次 prefetch 可以重试。 */
  inFlight: Promise<void> | null
  /** 已成功加载：chunk 进了模块缓存，再预取只是白等。 */
  loaded: boolean
}

export function createPrefetchRegistry(): PrefetchRegistry {
  const entries = new Map<string, PrefetchEntry>()

  return {
    register(id, load) {
      const existing = entries.get(id)
      if (!existing) {
        entries.set(id, { load, started: false, inFlight: null, loaded: false })
        return
      }
      // A re-register swaps where the *next* prefetch goes (hot reload, or a
      // view that rebuilt its loader). A request already in flight keeps its
      // own result: it must not be started twice.
      existing.load = load
    },

    prefetch(id) {
      const entry = entries.get(id)
      if (!entry || entry.inFlight || entry.loaded) return
      entry.started = true
      let request: Promise<unknown>
      try {
        request = entry.load()
      } catch {
        // A loader that throws synchronously (broken dynamic import) must not
        // escape into the hover/focus handler. Nothing is cached, so the next
        // intent retries.
        return
      }
      entry.inFlight = Promise.resolve(request).then(
        () => {
          entry.inFlight = null
          entry.loaded = true
        },
        () => {
          // Swallow the rejection here: opening the panel later runs the same
          // loader through Suspense/lazy, which reports the real error. Keeping
          // `loaded` false is what makes the retry path work.
          entry.inFlight = null
        },
      )
    },

    isStarted(id) {
      return entries.get(id)?.started ?? false
    },
  }
}
