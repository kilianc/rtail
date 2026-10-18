/*!
 * The landing page's own behaviour. There is very little of it, and none of it
 * is load-bearing: the page is complete and readable with this file blocked.
 * The copy buttons fall back to selectable text, and the rQL demo falls back to
 * its first query, which is already on screen.
 *
 * Plain JavaScript, served as written. The webapp is TypeScript and goes
 * through esbuild; sixty lines of DOM wiring do not need a bundler, and not
 * having one means the file you read here is the file the browser runs.
 */

/*!
 * copy buttons
 */

const RESET_AFTER = 1400

for (const button of document.querySelectorAll('button[data-copy]')) {
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
 * the rQL demo
 *
 * Every result set is in the markup already; this only decides which one is
 * visible. Nothing is generated here, so what the page shows after a click is
 * the same HTML a reader could have found with view-source — and a query whose
 * results were invented in JavaScript would be exactly the kind of claim this
 * page should not make.
 */

const demo = document.querySelector('[data-demo]')

if (demo) {
  const input = demo.querySelector('input')
  const count = demo.querySelector('[data-count]')
  const hint = demo.querySelector('p[data-hint]')
  const chips = demo.querySelectorAll('.chip[data-set]')
  const sets = demo.querySelectorAll('.demo-rows > [data-set]')

  for (const chip of chips) {
    chip.addEventListener('click', () => {
      for (const other of chips) {
        other.setAttribute('aria-pressed', String(other === chip))
      }

      for (const set of sets) {
        set.hidden = set.dataset.set !== chip.dataset.set
      }

      input.value = chip.textContent.trim()
      count.textContent = chip.dataset.count
      hint.textContent = chip.dataset.hint
    })
  }
}
