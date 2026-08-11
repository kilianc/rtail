import test, { afterEach, beforeEach, describe } from 'node:test'
import assert from 'node:assert/strict'
import dgram from 'node:dgram'
import { setTimeout as delay } from 'node:timers/promises'
import { createRtailServer, type RtailServer } from '../../../cli/lib/server.ts'
import { act, mount, setupDom, type Dom } from '../../helpers/dom.ts'
import { settleConnections } from '../../helpers/socket.ts'

/**
 * App is wired to a real rtail-server rather than a stubbed socket.
 *
 * The component's whole job is the conversation with that server — subscribe,
 * replay, append, unsubscribe on pause — and a hand-rolled fake would only
 * prove that the fake matches the assumptions in this file.
 */
describe('App', () => {
  let server: RtailServer
  let dom: Dom
  let udpPort: number
  let App: typeof import('../../../app/src/app.tsx').App

  beforeEach(async () => {
    server = createRtailServer({
      udpHost: '127.0.0.1',
      udpPort: 0,
      webHost: '127.0.0.1',
      webPort: 0,
      backlog: 100
    })

    await server.listening

    udpPort = (server.udp.address() as { port: number }).port
    dom = setupDom(`http://127.0.0.1:${server.address().port}/`)

    // Imported per test, after the DOM exists: socket.io-client picks its
    // transports from the environment present when it is first loaded.
    ;({ App } = await import('../../../app/src/app.tsx'))
  })

  afterEach(async () => {
    // cleanup() unmounts App, whose effect cleanup closes the socket; give the
    // server a moment to see that before shutting it down.
    dom.cleanup()
    await settleConnections(server.io)
    await server.close()
  })

  /** Sends a line to the server as the rtail client would. */
  function emit(id: string, content: unknown, timestamp = Date.now()): Promise<void> {
    const socket = dgram.createSocket('udp4')
    const buffer = Buffer.from(JSON.stringify({ id, timestamp, content }))

    return new Promise((resolve, reject) => {
      socket.send(buffer, 0, buffer.length, udpPort, '127.0.0.1', (err) => {
        socket.close()
        err ? reject(err) : resolve()
      })
    })
  }

  /** Polls until the DOM settles into the expected shape. */
  async function waitFor(predicate: () => boolean, what: string, timeoutMs = 5000): Promise<void> {
    const deadline = Date.now() + timeoutMs

    while (!predicate()) {
      if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`)
      await delay(20)
    }
  }

  const start = () => mount(<App />, dom.container)

  const streamLinks = () =>
    [...dom.container.querySelectorAll('.stream-section a span')].map((el) => el.textContent)

  const renderedLines = () =>
    [...dom.container.querySelectorAll('.stream-line-content')].map((el) => el.textContent)

  const click = async (el: Element | null) => {
    assert.ok(el, 'expected the element to exist')
    act(() => {
      ;(el as HTMLElement).click()
    })
    await delay(50)
  }

  test('starts with no stream selected', () => {
    start()

    assert.ok(dom.container.querySelector('.stream-empty'))
    assert.equal(dom.document.title, 'rTail')
  })

  test('lists streams as the server announces them', async () => {
    start()

    await emit('api-gateway', 'hello')
    await waitFor(() => streamLinks().includes('api-gateway'), 'the stream to appear')

    assert.deepEqual(streamLinks(), ['api-gateway'])
  })

  test('subscribes and replays the backlog when a stream is picked', async () => {
    await emit('api', 'first line')
    await emit('api', 'second line')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')

    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 2, 'the backlog to render')

    assert.deepEqual(renderedLines(), ['first line', 'second line'])
  })

  test('appends live lines', async () => {
    await emit('api', 'backlog')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 1, 'the backlog to render')

    await emit('api', 'live one')
    await waitFor(() => renderedLines().length === 2, 'the live line to render')

    assert.deepEqual(renderedLines(), ['backlog', 'live one'])
  })

  test('renders an object line as highlighted json', async () => {
    await emit('api', { level: 'warn' })

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 1, 'the line to render')

    assert.ok(dom.container.querySelector('.stream-line-content.object pre'))
  })

  test('reflects the selected stream in the title and the url', async () => {
    await emit('api', 'x')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))

    assert.equal(dom.document.title, 'rTail : api')
    assert.equal(dom.window.location.hash, '#/streams/api')
  })

  test('opens the stream named in the url on load', async () => {
    await emit('from-url', 'a line')

    dom.window.history.replaceState(null, '', '#/streams/from-url')

    start()
    await waitFor(() => renderedLines().length === 1, 'the backlog to render')

    assert.deepEqual(renderedLines(), ['a line'])
  })

  test('follows a back/forward navigation', async () => {
    await emit('one', 'line one')
    await emit('two', 'line two')

    start()
    await waitFor(() => streamLinks().length === 2, 'both streams to appear')

    dom.window.history.replaceState(null, '', '#/streams/two')
    act(() => {
      dom.window.dispatchEvent(new dom.window.Event('hashchange'))
    })

    await waitFor(() => renderedLines().includes('line two'), 'the routed stream to render')

    assert.equal(dom.document.title, 'rTail : two')
  })

  test('pauses when the viewport is scrolled, and stops receiving', async () => {
    await emit('api', 'first')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 1, 'the backlog to render')

    act(() => {
      dom.container.querySelector('.stream-lines')!.dispatchEvent(new dom.window.Event('wheel'))
    })

    await waitFor(
      () => /Paused/.test(dom.container.querySelector('.stream-status')?.textContent ?? ''),
      'the paused indicator'
    )

    // Pausing unsubscribes server-side, so nothing should arrive at all.
    await emit('api', 'while paused')
    await delay(300)

    assert.deepEqual(renderedLines(), ['first'])
  })

  test('resumes and pulls a fresh backlog', async () => {
    await emit('api', 'first')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 1, 'the backlog to render')

    act(() => {
      dom.container.querySelector('.stream-lines')!.dispatchEvent(new dom.window.Event('wheel'))
    })
    await waitFor(() => !!dom.container.querySelector('.btn-resume'), 'the resume button')

    await emit('api', 'missed while paused')
    await click(dom.container.querySelector('.btn-resume'))

    await waitFor(() => renderedLines().length === 2, 'the refreshed backlog')

    assert.deepEqual(renderedLines(), ['first', 'missed while paused'])
  })

  /** Types into the filter box. */
  function filter(value: string) {
    act(() => {
      const input = dom.container.querySelector('.filter-box input') as HTMLInputElement
      input.value = value
      input.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    })
  }

  test('filters the rendered lines by text', async () => {
    await emit('api', 'alpha')
    await emit('api', 'beta')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 2, 'the backlog to render')

    filter('alpha')

    assert.deepEqual(renderedLines(), ['alpha'])
  })

  test('filters by regexp when the term is slashed', async () => {
    await emit('api', 'alpha')
    await emit('api', 'beta')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 2, 'the backlog to render')

    // A bare `^a` is literal text under the query language; only the slashed
    // form is a pattern.
    filter('^a')
    assert.deepEqual(renderedLines(), [])

    filter('/^a/')
    assert.deepEqual(renderedLines(), ['alpha'])
  })

  test('filters object lines by a JSON field', async () => {
    await emit('api', { level: 'error', msg: 'boom' })
    await emit('api', { level: 'info', msg: 'fine' })

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 2, 'the backlog to render')

    filter('level:error')

    await waitFor(() => renderedLines().length === 1, 'the field filter to apply')
    assert.match(renderedLines()[0] ?? '', /boom/)
  })

  test('reports how many lines matched', async () => {
    await emit('api', 'alpha')
    await emit('api', 'beta')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 2, 'the backlog to render')

    filter('alpha')

    await waitFor(
      () => '1/2' === dom.container.querySelector('.filter-count')?.textContent,
      'the match count'
    )
  })

  test('marks the query hits in the rendered lines', async () => {
    await emit('api', 'a boom here')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 1, 'the backlog to render')

    filter('boom')

    await waitFor(() => !!dom.container.querySelector('mark'), 'the hit to be marked')
    assert.equal(dom.container.querySelector('mark')?.textContent, 'boom')
  })

  test('keeps filtering on the terms that parsed, and says the query is broken', async () => {
    await emit('api', 'boom')
    await emit('api', 'quiet')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 2, 'the backlog to render')

    filter('boom /unclosed(/')

    await waitFor(
      () => /invalid/.test(dom.container.querySelector('.filter-box')?.className ?? ''),
      'the box to be marked invalid'
    )
    assert.deepEqual(renderedLines(), ['boom'])
  })

  test('extracts chosen fields from object lines, and persists the choice', async () => {
    await emit('api', { level: 'error', noise: 'lots' })

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 1, 'the backlog to render')

    await click(dom.container.querySelector('.btn-fields'))
    await waitFor(() => !!dom.container.querySelector('.field-row'), 'the field census')

    const level = [...dom.container.querySelectorAll('.field-row')].find((el) =>
      /^level$/.test(el.querySelector('.field-path')?.textContent ?? '')
    )

    await click(level ?? null)

    await waitFor(() => !/noise/.test(renderedLines()[0] ?? ''), 'the payload to collapse to fields')

    const stored = JSON.parse(localStorage.getItem('rtail:prefs') ?? '{}')

    // Fields follow the stream, since they are a property of what it logs.
    assert.deepEqual(stored.fields, { api: ['level'] })
  })

  test('applies the persisted theme to the body', async () => {
    localStorage.setItem('rtail:prefs', JSON.stringify({ theme: 'light', fontSize: 6, fontFamily: 2 }))

    start()
    await delay(50)

    assert.equal(dom.document.body.className, 'light font-family-2 font-size-6')
  })

  test('persists a preference change', async () => {
    start()
    await delay(50)

    await click(dom.container.querySelector('.btn-settings'))
    await click(dom.container.querySelector('.btn-theme-light'))

    await waitFor(() => /light/.test(dom.document.body.className), 'the theme to apply')

    const stored = JSON.parse(localStorage.getItem('rtail:prefs') ?? '{}')

    assert.equal(stored.theme, 'light')
  })

  test('publishes the sidebar width as a css variable', async () => {
    start()
    await delay(50)

    assert.equal(
      dom.document.documentElement.style.getPropertyValue('--sidebar-w'),
      '240px'
    )
  })

  test('toggles the active stream as a favorite', async () => {
    await emit('api', 'x')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))

    await click(dom.container.querySelector('.stream-title'))
    await waitFor(
      () => !!dom.container.querySelector('.stream-title-favorite.on'),
      'the favorite marker'
    )

    const stored = JSON.parse(localStorage.getItem('rtail:prefs') ?? '{}')

    assert.deepEqual(stored.favorites, ['api'])
    assert.deepEqual(
      [...dom.container.querySelectorAll('.stream-section h4')].map((el) => el.textContent),
      ['Favorites']
    )
  })

  test('remembers the stream across a reload', async () => {
    await emit('api', 'x')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))

    assert.equal(localStorage.getItem('rtail:activeStream'), 'api')
  })

  test('caps the rendered buffer', async () => {
    // The client buffer matches the server backlog; both are 100.
    for (let i = 0; i < 105; i++) await emit('api', `line ${i}`)

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length > 0, 'the backlog to render')

    await delay(200)

    assert.ok(renderedLines().length <= 100, `rendered ${renderedLines().length} lines`)
  })

  test('toggles the timestamp column per stream', async () => {
    await emit('api', 'x')

    start()
    await waitFor(() => streamLinks().includes('api'), 'the stream to appear')
    await click(dom.container.querySelector('.stream-section a'))
    await waitFor(() => renderedLines().length === 1, 'the backlog to render')

    assert.equal(dom.container.querySelectorAll('.stream-line-timestamp').length, 1)

    await click(dom.container.querySelector('.btn-toggle-timestamp'))
    await waitFor(
      () => 0 === dom.container.querySelectorAll('.stream-line-timestamp').length,
      'the column to collapse'
    )

    const stored = JSON.parse(localStorage.getItem('rtail:prefs') ?? '{}')

    assert.deepEqual(stored.hiddenTimestamps, ['api'])
  })

  test('resizes the sidebar by dragging', async () => {
    start()
    await delay(50)

    const handle = dom.container.querySelector('.resize-handler')!

    const down = new dom.window.MouseEvent('pointerdown', { bubbles: true, cancelable: true })
    Object.defineProperty(down, 'pointerId', { value: 1 })

    const move = new dom.window.MouseEvent('pointermove', {
      bubbles: true,
      cancelable: true,
      clientX: 400
    })
    Object.defineProperty(move, 'pointerId', { value: 1 })

    act(() => {
      handle.dispatchEvent(down)
    })
    act(() => {
      handle.dispatchEvent(move)
    })

    await waitFor(
      () => '400px' === dom.document.documentElement.style.getPropertyValue('--sidebar-w'),
      'the sidebar width to update'
    )

    const stored = JSON.parse(localStorage.getItem('rtail:prefs') ?? '{}')

    assert.equal(stored.sidebarWidth, 400)
  })
})
