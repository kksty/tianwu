/** Scroll only the log viewport so the run selector and toolbar stay in place. */
export function scrollRunStreamToLatest(stream: HTMLElement | null): void {
  const viewport = stream?.closest<HTMLElement>('[data-radix-scroll-area-viewport]')
  viewport?.scrollTo({ top: viewport.scrollHeight, behavior: 'smooth' })
}
