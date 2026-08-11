/*!
 * The end-to-end smoke suite.
 *
 * A line is piped into the real `rtail` client bin, travels over UDP to the
 * real `rtail-server` bin, and has to come out the other side rendered in a
 * real browser. If any link in that chain breaks, this is what says so.
 */

import { spawn } from 'node:child_process'
import { expect, test } from '@playwright/test'
import { UDP_PORT } from '../../playwright.config.ts'

/** Pipes lines through the real client bin, exactly as a shell would. */
function pipe(stream: string, lines: string[]): Promise<void> {
  const child = spawn(
    process.execPath,
    ['cli/rtail-client.ts', '--port', String(UDP_PORT), '--id', stream, '--mute'],
    { env: { ...process.env, NO_UPDATE_NOTIFIER: '1' } }
  )

  child.stdin.end(lines.join('\n') + '\n')

  return new Promise((resolve, reject) => {
    child.once('error', reject)
    child.once('close', () => resolve())
  })
}

/** Stream names are per-test so a rerun never collides with earlier state. */
let counter = 0
const streamName = (label: string) => `e2e-${label}-${++counter}`

test('renders a line piped through the client', async ({ page }) => {
  const stream = streamName('basic')

  await page.goto('/')
  await expect(page.locator('.stream-empty')).toBeVisible()

  await pipe(stream, ['hello from the pipe'])

  await page.getByRole('link', { name: stream }).click()

  await expect(page.locator('.stream-line-content')).toHaveText('hello from the pipe')
  await expect(page).toHaveTitle(`rTail : ${stream}`)
})

test('opens a stream from a deep link', async ({ page }) => {
  const stream = streamName('deeplink')

  await pipe(stream, ['line one', 'line two'])

  await page.goto(`/#/streams/${stream}`)

  await expect(page.locator('.stream-line-content')).toHaveText(['line one', 'line two'])
})

test('appends lines as they arrive', async ({ page }) => {
  const stream = streamName('live')

  await pipe(stream, ['first'])
  await page.goto(`/#/streams/${stream}`)
  await expect(page.locator('.stream-line-content')).toHaveCount(1)

  await pipe(stream, ['second'])

  await expect(page.locator('.stream-line-content')).toHaveText(['first', 'second'])
})

test('renders ansi colour as themed classes, not inline styles', async ({ page }) => {
  const stream = streamName('ansi')

  await pipe(stream, ['\u001b[32mgreen text\u001b[0m'])
  await page.goto(`/#/streams/${stream}`)

  const coloured = page.locator('.stream-line-content .ansi-green-fg')

  await expect(coloured).toHaveText('green text')
})

test('escapes markup in a log line', async ({ page }) => {
  const stream = streamName('xss')

  // Log lines are whatever a piped process printed: untrusted by definition.
  await pipe(stream, ['<img src=x onerror=alert(1)>'])
  await page.goto(`/#/streams/${stream}`)

  await expect(page.locator('.stream-line-content')).toHaveText('<img src=x onerror=alert(1)>')
  await expect(page.locator('.stream-line-content img')).toHaveCount(0)
})

test('pretty-prints a json line', async ({ page }) => {
  const stream = streamName('json')

  await pipe(stream, [JSON.stringify({ level: 'warn', count: 3 })])
  await page.goto(`/#/streams/${stream}`)

  await expect(page.locator('.stream-line-content.object pre')).toBeVisible()
  await expect(page.locator('.stream-line-content .hljs-attr').first()).toBeVisible()
})

test('filters the visible lines', async ({ page }) => {
  const stream = streamName('filter')

  await pipe(stream, ['alpha one', 'beta two', 'alpha three'])
  await page.goto(`/#/streams/${stream}`)
  await expect(page.locator('.stream-line-content')).toHaveCount(3)

  await page.getByLabel('Filter lines').fill('alpha')

  await expect(page.locator('.stream-line-body')).toHaveText(['alpha one', 'alpha three'])
  await expect(page.locator('.filter-count')).toHaveText('2/3')
})

test('marks the query hits', async ({ page }) => {
  const stream = streamName('mark')

  await pipe(stream, ['a boom here'])
  await page.goto(`/#/streams/${stream}`)

  await page.getByLabel('Filter lines').fill('boom')

  await expect(page.locator('.stream-line-body mark')).toHaveText('boom')
})

test('filters object lines by a JSON field', async ({ page }) => {
  const stream = streamName('field')

  await pipe(stream, [
    JSON.stringify({ level: 'error', msg: 'boom' }),
    JSON.stringify({ level: 'info', msg: 'fine' })
  ])
  await page.goto(`/#/streams/${stream}`)
  await expect(page.locator('.stream-line-content')).toHaveCount(2)

  // The point of the query language: this must not match the info line just
  // because the word "error" appears somewhere in its text.
  await page.getByLabel('Filter lines').fill('level:error')

  await expect(page.locator('.stream-line-content')).toHaveCount(1)
  await expect(page.locator('.stream-line-body')).toContainText('boom')
})

test('reports a query that will not parse without blanking the view', async ({ page }) => {
  const stream = streamName('badquery')

  await pipe(stream, ['boom', 'quiet'])
  await page.goto(`/#/streams/${stream}`)
  await expect(page.locator('.stream-line-content')).toHaveCount(2)

  await page.getByLabel('Filter lines').fill('boom /unclosed(/')

  await expect(page.locator('.filter-box')).toHaveClass(/invalid/)
  await expect(page.locator('.stream-line-body')).toHaveText(['boom'])
})

test('shows the filter syntax card', async ({ page }) => {
  await page.goto('/')

  const stream = streamName('help')
  await pipe(stream, ['x'])
  await page.getByRole('link', { name: stream }).click()

  await page.getByLabel('Filter syntax').click()

  await expect(page.locator('.popover-help')).toBeVisible()
  await expect(page.locator('.popover-help dt').first()).toBeVisible()
})

test('extracts JSON fields and collapses the payload to them', async ({ page }) => {
  const stream = streamName('fields')

  await pipe(stream, [JSON.stringify({ level: 'error', noise: 'lots of it', n: 1 })])
  await page.goto(`/#/streams/${stream}`)
  await expect(page.locator('.stream-line-body')).toContainText('noise')

  await page.getByRole('button', { name: /^Fields/ }).click()
  await page.locator('.field-row', { hasText: 'level' }).first().click()

  await expect(page.locator('.stream-line-body')).toContainText('level')
  await expect(page.locator('.stream-line-body')).not.toContainText('noise')

  // The picker offers the buffer's own field census, not a fixed schema.
  await expect(page.locator('.btn-fields-count')).toHaveText('1')
})

test('collapses and expands an object payload', async ({ page }) => {
  const stream = streamName('json-toggle')

  await pipe(stream, [JSON.stringify({ a: 1, b: 2 })])
  await page.goto(`/#/streams/${stream}`)

  const toggle = page.getByLabel(/^(Collapse|Expand) payload$/)
  await expect(toggle).toBeVisible()

  await toggle.click()
  await expect(page.getByLabel('Expand payload')).toBeVisible()

  await page.getByLabel('Expand payload').click()
  await expect(page.getByLabel('Collapse payload')).toBeVisible()
})

test('finds a stream through the sidebar search', async ({ page }) => {
  const stream = streamName('search')
  const other = streamName('other')

  await pipe(stream, ['x'])
  await pipe(other, ['y'])

  await page.goto('/')
  await expect(page.getByRole('link', { name: stream })).toBeVisible()

  await page.getByLabel('Search streams').fill(stream)

  await expect(page.getByRole('link', { name: stream })).toBeVisible()
  await expect(page.getByRole('link', { name: other })).toHaveCount(0)
})

test('switches theme and remembers it across a reload', async ({ page }) => {
  await page.goto('/')

  await expect(page.locator('body')).toHaveClass(/dark/)

  await page.getByLabel('Settings').click()
  await page.getByLabel('light theme').click()

  await expect(page.locator('body')).toHaveClass(/light/)

  await page.reload()

  await expect(page.locator('body')).toHaveClass(/light/)
})

test('reverses the sort order', async ({ page }) => {
  const stream = streamName('sort')

  await pipe(stream, ['first', 'second'])
  await page.goto(`/#/streams/${stream}`)
  await expect(page.locator('.stream-line-content')).toHaveText(['first', 'second'])

  await page.getByLabel('Settings').click()
  await page.getByLabel('Newest first').click()

  await expect(page.locator('.stream-line-content')).toHaveText(['second', 'first'])
})

test('favorites a stream and lifts it into its own section', async ({ page }) => {
  const stream = streamName('fav')

  await pipe(stream, ['x'])
  await page.goto(`/#/streams/${stream}`)

  await page.locator('.stream-title').click()

  await expect(page.locator('.stream-section h4').first()).toHaveText('Favorites')
  await expect(page.locator('.stream-title-favorite.on')).toBeVisible()
})

test('pauses on scroll and resumes on demand', async ({ page }) => {
  const stream = streamName('pause')

  await pipe(stream, ['before pause'])

  // Reached from the empty state via the sidebar, not a deep link. The wheel
  // listener is attached by an effect that only re-runs when the scroller
  // appears; keyed wrongly, scroll-to-pause worked on a deep link and did
  // nothing at all on this path.
  await page.goto('/')
  await page.getByRole('link', { name: stream }).click()
  await expect(page.locator('.stream-line-content')).toHaveCount(1)

  await page.locator('.stream-lines').hover()
  await page.mouse.wheel(0, -200)

  await expect(page.locator('.stream-status')).toHaveText('Paused')

  // Pausing unsubscribes server-side, so this must not appear yet.
  await pipe(stream, ['while paused'])
  await expect(page.locator('.stream-line-content')).toHaveCount(1)

  await page.getByRole('button', { name: 'Resume' }).click()

  await expect(page.locator('.stream-status')).toHaveText('Live')
  await expect(page.locator('.stream-line-content')).toHaveText(['before pause', 'while paused'])
})

test('collapses the timestamp column', async ({ page }) => {
  const stream = streamName('gutter')

  await pipe(stream, ['x'])
  await page.goto(`/#/streams/${stream}`)
  await expect(page.locator('.stream-line-timestamp')).toHaveCount(1)

  await page.getByLabel('Toggle timestamp column').click()

  await expect(page.locator('.stream-line-timestamp')).toHaveCount(0)
})

test('reports the build version', async ({ page }) => {
  await page.goto('/')

  await page.getByLabel('About rTail').click()

  // __VERSION__ is substituted by esbuild at bundle time; if that ever broke,
  // this would read "Version undefined".
  await expect(page.locator('.version')).toHaveText(/^Version \d+\.\d+\.\d+/)
})

test('loads with no console errors', async ({ page }) => {
  // Kept as two lists rather than one. An uncaught exception is an app bug;
  // a console error can also be the browser reporting a transport hiccup that
  // socket.io then recovers from. Merging them makes a failure ambiguous, and
  // the distinction is the first thing you want to know.
  const uncaught: string[] = []
  const logged: string[] = []

  page.on('pageerror', (error) => uncaught.push(`${error.name}: ${error.message}`))
  page.on('console', (message) => {
    if ('error' === message.type()) logged.push(`${message.text()} @ ${message.location().url}`)
  })

  const stream = streamName('clean')
  await pipe(stream, ['a line'])

  await page.goto(`/#/streams/${stream}`)
  await expect(page.locator('.stream-line-content')).toHaveCount(1)

  expect(uncaught, 'uncaught exceptions on the page').toEqual([])
  expect(logged, 'console.error output').toEqual([])
})
