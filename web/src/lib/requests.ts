/** Revalidation joins the current request; changed queries and manual refresh
 * replace it. Retired responses cannot update the UI. */
export function createRequestGate<T>() {
  let generation = 0
  let pending: Promise<void> | undefined
  return {
    invalidate() { generation++; pending = undefined },
    run(load: () => Promise<T>, handlers: {
      start: () => void
      value: (value: T) => void
      error: (error: unknown) => void
      finish: () => void
    }, replace = false): Promise<void> {
      if (pending && !replace) return pending
      const current = ++generation
      handlers.start()
      pending = Promise.resolve().then(load).then(
        value => { if (current === generation) handlers.value(value) },
        error => { if (current === generation) handlers.error(error) },
      ).finally(() => {
        if (current !== generation) return
        pending = undefined
        handlers.finish()
      })
      return pending
    },
  }
}
