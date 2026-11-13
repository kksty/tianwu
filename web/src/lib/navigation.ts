const navigationEvent = 'tw:navigate'

export function locationPath(): string {
  return window.location.pathname + window.location.search + window.location.hash
}

/** Same-document navigation must notify React as well as the browser history. */
export function navigate(url: string, replace = false): void {
  if (url === locationPath()) return
  if (replace) window.history.replaceState(null, '', url)
  else window.history.pushState(null, '', url)
  window.dispatchEvent(new Event(navigationEvent))
}

export function subscribeLocation(changed: () => void): () => void {
  const events = ['popstate', 'hashchange', navigationEvent]
  for (const event of events) window.addEventListener(event, changed)
  return () => { for (const event of events) window.removeEventListener(event, changed) }
}

/** A damaged or hand-written escape must not prevent the workspace rendering. */
export function decodeLocationPart(value: string): string {
  try { return decodeURIComponent(value) }
  catch { return value }
}
