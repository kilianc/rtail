/*!
 * The landing page's own behaviour. There is very little of it.
 *
 * Everything here is progressive: the page is complete and readable with this
 * file blocked. The copy buttons fall back to selectable text, and the preview
 * boots in the app's default theme without the toggle.
 */

/*!
 * copy buttons
 */

const RESET_AFTER = 1400

for (const button of document.querySelectorAll<HTMLButtonElement>('button[data-copy]')) {
  const text = button.dataset.copy

  if (!text || !navigator.clipboard) {
    button.hidden = true
    continue
  }

  const label = button.textContent

  button.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      // Permission denied, or an insecure context. Saying nothing is better
      // than an error dialog over a convenience.
      return
    }

    button.dataset.copied = ''
    button.textContent = 'Copied'

    setTimeout(() => {
      delete button.dataset.copied
      button.textContent = label
    }, RESET_AFTER)
  })
}

/*!
 * preview theme
 *
 * The preview is a same-origin iframe running the real app, and the app reads
 * its theme from localStorage on boot (see app/src/lib/prefs.ts). So the way
 * to drive it from out here is to write the preference the app already looks
 * for and reload the frame — no message channel, and no change to app/src.
 */

const PREFS_KEY = 'rtail:prefs'

const frame = document.querySelector<HTMLIFrameElement>('.window-frame')
const toggle = document.querySelector<HTMLButtonElement>('[data-preview-theme]')

if (frame && toggle) {
  let theme: 'dark' | 'light' = 'dark'

  toggle.addEventListener('click', () => {
    theme = 'dark' === theme ? 'light' : 'dark'

    try {
      const stored = JSON.parse(localStorage.getItem(PREFS_KEY) ?? '{}') as Record<string, unknown>
      localStorage.setItem(PREFS_KEY, JSON.stringify({ ...stored, theme }))
    } catch {
      // Storage disabled — the frame simply keeps the theme it has.
      return
    }

    // Reloading rather than re-assigning src: the URL carries a hash, and
    // assigning an identical src is a no-op in some browsers.
    frame.contentWindow?.location.reload()

    toggle.textContent = 'dark' === theme ? 'Light' : 'Dark'
    toggle.setAttribute('aria-pressed', String('light' === theme))
  })
}
