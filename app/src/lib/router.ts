/*!
 * A hash router for exactly one route: #/streams/:name
 *
 * Replaces angular-ui-router, which was ~140x the size of this file and, at
 * the version the project floated to, silently aborted transitions when a
 * template filter threw.
 */

const PREFIX = '#/streams/'

/** @returns the stream named in the current URL, if any. */
export function readStream(): string | null {
  const hash = window.location.hash

  if (!hash.startsWith(PREFIX)) return null

  const name = decodeURIComponent(hash.slice(PREFIX.length))

  return name || null
}

/** Updates the URL without adding a history entry for every stream switch. */
export function writeStream(stream: string | null): void {
  const next = null === stream ? '#/' : PREFIX + encodeURIComponent(stream)

  if (window.location.hash === next) return

  window.history.replaceState(null, '', next)
}

/** Subscribes to back/forward navigation and manual URL edits. */
export function onRouteChange(handler: (stream: string | null) => void): () => void {
  const listener = () => handler(readStream())

  window.addEventListener('hashchange', listener)

  return () => window.removeEventListener('hashchange', listener)
}
