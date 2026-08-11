import test, { afterEach, beforeEach, describe } from 'node:test'
import assert from 'node:assert/strict'
import { StreamView } from '../../../app/src/components/StreamView.tsx'
import type { Line } from '../../../app/src/lib/types.ts'
import { act, fireKey, mount, setupDom, type Dom } from '../../helpers/dom.ts'

let nextKey = 0

function line(text: string, overrides: Partial<Line> = {}): Line {
  return {
    timestamp: Date.UTC(2024, 5, 5, 10, 0, 0),
    streamid: 'api',
    host: '127.0.0.1',
    port: 1234,
    content: text,
    type: 'string',
    html: text,
    text,
    key: nextKey++,
    ...overrides
  }
}

describe('StreamView', () => {
  let dom: Dom

  const noop = () => {}

  const base = {
    activeStream: 'api' as string | null,
    lines: [] as Line[],
    filter: '',
    ascending: true,
    timestampsHidden: false,
    paused: false,
    onToggleTimestamps: noop,
    onPause: noop,
    onResume: noop
  }

  beforeEach(() => {
    dom = setupDom()
  })

  afterEach(() => dom.cleanup())

  const show = (props: Partial<typeof base> = {}) =>
    mount(<StreamView {...base} {...props} />, dom.container)

  const texts = () =>
    [...dom.container.querySelectorAll('.stream-line-content')].map((el) => el.innerHTML)

  test('prompts for a stream when none is selected', () => {
    show({ activeStream: null })

    assert.ok(dom.container.querySelector('.stream-empty'))
    assert.match(dom.container.textContent ?? '', /Select a stream/)
  })

  test('renders one row per line', () => {
    show({ lines: [line('one'), line('two')] })

    assert.deepEqual(texts(), ['one', 'two'])
  })

  test('renders the pre-computed html rather than escaping it again', () => {
    show({ lines: [line('x', { html: '<span class="ansi-red-fg">boom</span>' })] })

    assert.ok(dom.container.querySelector('.ansi-red-fg'))
  })

  test('tags the row with the content type', () => {
    show({ lines: [line('{}', { type: 'object' })] })

    assert.match(dom.container.querySelector('.stream-line-content')!.className, /object/)
  })

  test('applies the filter', () => {
    show({ lines: [line('alpha'), line('beta')], filter: '^a' })

    assert.deepEqual(texts(), ['alpha'])
  })

  test('shows everything again when the filter will not compile', () => {
    show({ lines: [line('alpha'), line('beta')], filter: '[' })

    assert.deepEqual(texts(), ['alpha', 'beta'])
  })

  test('reverses the order when sorting newest first', () => {
    show({ lines: [line('first'), line('second')], ascending: false })

    assert.deepEqual(texts(), ['second', 'first'])
  })

  test('shows the timestamp column by default', () => {
    show({ lines: [line('x')] })

    assert.equal(dom.container.querySelectorAll('.stream-line-timestamp').length, 1)
    assert.doesNotMatch(dom.container.querySelector('.stream-view')!.className, /no-timestamps/)
  })

  test('hides the timestamp column on request', () => {
    show({ lines: [line('x')], timestampsHidden: true })

    assert.equal(dom.container.querySelectorAll('.stream-line-timestamp').length, 0)
    assert.match(dom.container.querySelector('.stream-view')!.className, /no-timestamps/)
  })

  test('reports the timestamp toggle', () => {
    let toggled = 0
    show({ onToggleTimestamps: () => toggled++ })

    act(() => {
      ;(dom.container.querySelector('.btn-toggle-timestamp') as HTMLElement).click()
    })

    assert.equal(toggled, 1)
  })

  test('marks the toggle as pressed while the column is hidden', () => {
    show({ timestampsHidden: true })

    assert.equal(
      dom.container.querySelector('.btn-toggle-timestamp')!.getAttribute('aria-pressed'),
      'true'
    )
  })

  test('offers a resume button only while paused', () => {
    show()
    assert.equal(dom.container.querySelector('.btn-resume'), null)

    show({ paused: true })
    assert.ok(dom.container.querySelector('.btn-resume'))
  })

  test('resumes when the button is clicked', () => {
    let resumed = 0
    show({ paused: true, onResume: () => resumed++ })

    act(() => {
      ;(dom.container.querySelector('.btn-resume') as HTMLElement).click()
    })

    assert.equal(resumed, 1)
  })

  test('pauses when the viewport is scrolled away from the tail', () => {
    let paused = 0
    show({ lines: [line('x')], onPause: () => paused++ })

    act(() => {
      dom.container.querySelector('.stream-lines')!.dispatchEvent(new dom.window.Event('wheel'))
    })

    assert.equal(paused, 1)
  })

  test('resumes on the space bar while paused', () => {
    let resumed = 0
    show({ paused: true, onResume: () => resumed++ })

    fireKey(dom, dom.window, ' ')

    assert.equal(resumed, 1)
  })

  test('ignores the space bar while live', () => {
    let resumed = 0
    show({ paused: false, onResume: () => resumed++ })

    fireKey(dom, dom.window, ' ')

    assert.equal(resumed, 0)
  })

  test('ignores other keys while paused', () => {
    let resumed = 0
    show({ paused: true, onResume: () => resumed++ })

    fireKey(dom, dom.window, 'a')

    assert.equal(resumed, 0)
  })

  test('leaves the space bar alone while typing in a field', () => {
    let resumed = 0
    show({ paused: true, onResume: () => resumed++ })

    // Otherwise a space in the filter box would resume the stream.
    const input = dom.document.createElement('input')
    dom.document.body.appendChild(input)
    fireKey(dom, input, ' ')

    assert.equal(resumed, 0)
  })
})
