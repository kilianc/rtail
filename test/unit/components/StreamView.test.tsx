import test, { afterEach, beforeEach, describe } from 'node:test'
import assert from 'node:assert/strict'
import { StreamView } from '../../../app/src/components/StreamView.tsx'
import type { Needle } from '../../../app/src/lib/query.ts'
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
    htmlCompact: null,
    bulky: false,
    text,
    key: nextKey++,
    ...overrides
  }
}

/** An object line, with the pretty and compact renderings StreamView expects. */
function objectLine(content: Record<string, unknown>, bulky = false): Line {
  return line(JSON.stringify(content), {
    content,
    type: 'object',
    html: `<pre>${JSON.stringify(content, null, '  ')}</pre>`,
    htmlCompact: `<pre>${JSON.stringify(content)}</pre>`,
    bulky
  })
}

describe('StreamView', () => {
  let dom: Dom

  const noop = () => {}

  const base = {
    activeStream: 'api' as string | null,
    lines: [] as Line[],
    needles: [] as Needle[],
    fields: [] as string[],
    jsonView: 'auto' as 'auto' | 'collapsed' | 'expanded',
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

  const bodies = () =>
    [...dom.container.querySelectorAll('.stream-line-body')].map((el) => el.innerHTML)

  const texts = () =>
    [...dom.container.querySelectorAll('.stream-line-body')].map((el) => el.textContent)

  test('prompts for a stream when none is selected', () => {
    show({ activeStream: null })

    assert.ok(dom.container.querySelector('.stream-empty'))
    assert.match(dom.container.textContent ?? '', /Select a stream/)
  })

  test('renders one row per line, in the order given', () => {
    // App filters and orders; StreamView renders what it is handed.
    show({ lines: [line('one'), line('two')] })

    assert.deepEqual(texts(), ['one', 'two'])
  })

  test('renders the pre-computed html rather than escaping it again', () => {
    show({ lines: [line('x', { html: '<span class="ansi-red-fg">boom</span>' })] })

    assert.ok(dom.container.querySelector('.ansi-red-fg'))
  })

  test('tags the row with the content type', () => {
    show({ lines: [objectLine({ a: 1 })] })

    assert.match(dom.container.querySelector('.stream-line-content')!.className, /object/)
  })

  test('marks the query hits', () => {
    const needles: Needle[] = [{ type: 'literal', value: 'boom', caseSensitive: false }]
    show({ lines: [line('a boom here')], needles })

    assert.equal(dom.container.querySelector('.stream-line-body mark')?.textContent, 'boom')
  })

  test('leaves the line unmarked when the box is empty', () => {
    show({ lines: [line('a boom here')] })

    assert.equal(dom.container.querySelector('mark'), null)
  })

  describe('timestamps', () => {
    test('shows the column by default', () => {
      show({ lines: [line('x')] })

      assert.equal(dom.container.querySelectorAll('.stream-line-timestamp').length, 1)
      assert.doesNotMatch(dom.container.querySelector('.stream-view')!.className, /no-timestamps/)
    })

    test('hides the column on request', () => {
      show({ lines: [line('x')], timestampsHidden: true })

      assert.equal(dom.container.querySelectorAll('.stream-line-timestamp').length, 0)
      assert.match(dom.container.querySelector('.stream-view')!.className, /no-timestamps/)
    })

    test('reports the toggle', () => {
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
  })

  describe('object payloads', () => {
    test('offers a caret only on object lines', () => {
      show({ lines: [line('plain'), objectLine({ a: 1 })] })

      assert.equal(dom.container.querySelectorAll('.json-toggle').length, 1)
    })

    test('expands a small payload on auto', () => {
      show({ lines: [objectLine({ a: 1 })] })

      assert.match(dom.container.querySelector('.stream-line-content')!.className, /expanded/)
      assert.match(bodies()[0]!, /\n/)
    })

    test('collapses a bulky payload on auto', () => {
      show({ lines: [objectLine({ a: 1 }, true)] })

      assert.match(dom.container.querySelector('.stream-line-content')!.className, /collapsed/)
      assert.doesNotMatch(bodies()[0]!, /\n/)
    })

    test('honours an explicit collapsed preference', () => {
      show({ lines: [objectLine({ a: 1 })], jsonView: 'collapsed' })

      assert.match(dom.container.querySelector('.stream-line-content')!.className, /collapsed/)
    })

    test('honours an explicit expanded preference, even when bulky', () => {
      show({ lines: [objectLine({ a: 1 }, true)], jsonView: 'expanded' })

      assert.match(dom.container.querySelector('.stream-line-content')!.className, /expanded/)
    })

    test('toggles one line without disturbing the rest', () => {
      show({ lines: [objectLine({ a: 1 }), objectLine({ b: 2 })], jsonView: 'collapsed' })

      act(() => {
        ;(dom.container.querySelector('.json-toggle') as HTMLElement).click()
      })

      const classes = [...dom.container.querySelectorAll('.stream-line-content')].map(
        (el) => el.className
      )

      assert.match(classes[0]!, /expanded/)
      assert.match(classes[1]!, /collapsed/)
    })

    test('labels the caret by what it will do', () => {
      show({ lines: [objectLine({ a: 1 })], jsonView: 'collapsed' })

      const caret = dom.container.querySelector('.json-toggle')!
      assert.equal(caret.getAttribute('aria-label'), 'Expand payload')

      act(() => {
        ;(caret as HTMLElement).click()
      })

      assert.equal(
        dom.container.querySelector('.json-toggle')!.getAttribute('aria-label'),
        'Collapse payload'
      )
    })

    test('forgets the per-line toggles when the stream changes', () => {
      show({ lines: [objectLine({ a: 1 })], jsonView: 'collapsed' })

      act(() => {
        ;(dom.container.querySelector('.json-toggle') as HTMLElement).click()
      })
      assert.match(dom.container.querySelector('.stream-line-content')!.className, /expanded/)

      show({ activeStream: 'other', lines: [objectLine({ a: 1 })], jsonView: 'collapsed' })

      assert.match(dom.container.querySelector('.stream-line-content')!.className, /collapsed/)
    })
  })

  describe('extracted fields', () => {
    test('shows just the chosen fields instead of the payload', () => {
      show({ lines: [objectLine({ level: 'error', noise: 'lots' })], fields: ['level'] })

      const body = bodies()[0]!

      assert.match(body, /level/)
      assert.match(body, /error/)
      assert.doesNotMatch(body, /noise/)
    })

    test('falls back to the payload for a line carrying none of them', () => {
      show({ lines: [objectLine({ other: 1 })], fields: ['level'] })

      assert.match(bodies()[0]!, /other/)
    })

    test('still lets a line be expanded back to the full payload', () => {
      show({ lines: [objectLine({ level: 'error', noise: 'lots' })], fields: ['level'] })

      act(() => {
        ;(dom.container.querySelector('.json-toggle') as HTMLElement).click()
      })

      assert.match(bodies()[0]!, /noise/)
    })
  })

  describe('pause and resume', () => {
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
})
