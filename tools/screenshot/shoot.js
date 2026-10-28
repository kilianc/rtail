/*!
 * shoot.js — the README screenshot, in both themes.
 *
 *   node tools/screenshot/shoot.js http://localhost:8888
 *
 * Writes docs/media/explorer-dark.png and explorer-light.png at 1600x940, at
 * twice that in pixels. The README's <picture> shows the one that matches the
 * reader's GitHub theme, which is why there are two: the dark one alone is a
 * black slab on a white page, and the light one alone is the reverse.
 *
 * The view is a URL, like every view in the app. The theme is not — it is a
 * preference, kept in localStorage — so it is written there before the page
 * loads, under the key app/src/lib/prefs.ts reads.
 */

import { createRequire } from 'node:module'

// Playwright is installed globally in the screenshot image, and an ESM import
// does not look at NODE_PATH; require does.
const { chromium } = createRequire(import.meta.url)('playwright')

const BASE = process.argv[2] || 'http://localhost:8888'

const VIEW = new URLSearchParams({
  stream: 'api',
  q: 'level>=WARN',
  from: '-1h',
  live: 'false',
  cols: 'service,status,latency_ms,region'
})

/*!
 * Wait until the flush has landed. Ingest is acknowledged from the
 * write-ahead log, and search reads Parquet, which the store writes on its
 * next flush — up to thirty seconds after the seed finished.
 */
async function flushed() {
  const query = new URLSearchParams({ stream: 'api', from: '-2h', limit: '1' })

  for (let attempt = 0; attempt < 60; attempt++) {
    const response = await fetch(`${BASE}/v1/search?${query}`)
    if (response.ok && (await response.json()).scanned?.files > 0) return
    await new Promise((resolve) => setTimeout(resolve, 1000))
  }

  throw new Error('the seeded records never reached a Parquet file')
}

await flushed()

const browser = await chromium.launch()

for (const theme of ['dark', 'light']) {
  const page = await browser.newPage({
    viewport: { width: 1600, height: 940 },
    deviceScaleFactor: 2,
    colorScheme: theme
  })

  await page.addInitScript((theme) => {
    localStorage.setItem('rtail:prefs:v2', JSON.stringify({ theme }))
  }, theme)

  await page.goto(`${BASE}/#${VIEW}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('.row')

  // Fonts come from Google and the histogram animates in; neither is
  // something networkidle waits for.
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(1000)

  await page.screenshot({ path: `docs/media/explorer-${theme}.png` })
  console.log(`  docs/media/explorer-${theme}.png`)

  await page.close()
}

await browser.close()
