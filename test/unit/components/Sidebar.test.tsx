import test, { afterEach, beforeEach, describe } from 'node:test'
import assert from 'node:assert/strict'
import { Sidebar } from '../../../app/src/components/Sidebar.tsx'
import { act, fireKey, firePointer, mount, setupDom, type Dom } from '../../helpers/dom.ts'

describe('Sidebar', () => {
  let dom: Dom

  const noop = () => {}

  const base = {
    streams: [] as string[],
    favorites: [] as string[],
    activeStream: null as string | null,
    onSelect: noop as (stream: string) => void,
    onResize: noop as (width: number) => void
  }

  beforeEach(() => {
    dom = setupDom()
  })

  afterEach(() => dom.cleanup())

  const show = (props: Partial<typeof base> = {}) =>
    mount(<Sidebar {...base} {...props} />, dom.container)

  const search = () => dom.container.querySelector('input') as HTMLInputElement

  const sections = () =>
    [...dom.container.querySelectorAll('.stream-section')].map((section) => ({
      title: section.querySelector('h4')?.textContent,
      streams: [...section.querySelectorAll('a span')].map((el) => el.textContent)
    }))

  /** Types into the search box the way preact's onInput sees it. */
  function type(value: string) {
    act(() => {
      search().value = value
      search().dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    })
  }

  test('says so when there are no streams', () => {
    show()

    assert.match(dom.container.querySelector('.no-matches')!.textContent ?? '', /No streams yet/)
  })

  test('lists streams alphabetically', () => {
    show({ streams: ['worker', 'api', 'nginx'] })

    assert.deepEqual(sections(), [{ title: 'Streams', streams: ['api', 'nginx', 'worker'] }])
  })

  test('lifts favorites into their own section', () => {
    show({ streams: ['worker', 'api'], favorites: ['api'] })

    assert.deepEqual(sections(), [
      { title: 'Favorites', streams: ['api'] },
      { title: 'Streams', streams: ['worker'] }
    ])
  })

  test('lists a favorite that has not reported a line yet', () => {
    // Favorites are persisted, so one can outlive the stream it names.
    show({ streams: [], favorites: ['gone'] })

    assert.deepEqual(sections(), [{ title: 'Favorites', streams: ['gone'] }])
  })

  test('marks the active stream', () => {
    show({ streams: ['api', 'worker'], activeStream: 'worker' })

    const active = dom.container.querySelector('a.selected')

    assert.equal(active?.textContent, 'worker')
    assert.equal(active?.getAttribute('aria-current'), 'true')
  })

  test('selects a stream without following the link', () => {
    const selected: string[] = []
    show({ streams: ['api'], onSelect: (s) => selected.push(s) })

    const link = dom.container.querySelector('a') as HTMLAnchorElement
    let defaultPrevented = false

    act(() => {
      const event = new dom.window.MouseEvent('click', { bubbles: true, cancelable: true })
      link.dispatchEvent(event)
      defaultPrevented = event.defaultPrevented
    })

    assert.deepEqual(selected, ['api'])
    assert.ok(defaultPrevented, 'the hash is written by the app, not by the browser')
  })

  test('still carries a real href for middle-click and copy-link', () => {
    show({ streams: ['api/v2'] })

    assert.equal(
      dom.container.querySelector('a')?.getAttribute('href'),
      '#/streams/api%2Fv2'
    )
  })

  test('filters both sections as you type', () => {
    show({ streams: ['api', 'worker'], favorites: ['api-gateway'] })

    type('api')

    assert.deepEqual(sections(), [
      { title: 'Favorites', streams: ['api-gateway'] },
      { title: 'Streams', streams: ['api'] }
    ])
  })

  test('matches case-insensitively and ignores surrounding spaces', () => {
    show({ streams: ['API-Gateway'] })

    type('  gateway  ')

    assert.deepEqual(sections(), [{ title: 'Streams', streams: ['API-Gateway'] }])
  })

  test('reports a query that matches nothing', () => {
    show({ streams: ['api'] })

    type('zzz')

    assert.match(dom.container.querySelector('.no-matches')!.textContent ?? '', /No streams match/)
  })

  test('offers a clear button only once something is typed', () => {
    show({ streams: ['api'] })
    assert.equal(dom.container.querySelector('.search-clear'), null)

    type('api')
    assert.ok(dom.container.querySelector('.search-clear'))
  })

  test('clears the query with the button', () => {
    show({ streams: ['api', 'worker'] })
    type('api')

    act(() => {
      ;(dom.container.querySelector('.search-clear') as HTMLElement).click()
    })

    assert.equal(search().value, '')
    assert.deepEqual(sections(), [{ title: 'Streams', streams: ['api', 'worker'] }])
  })

  test('clears the query on Escape', () => {
    show({ streams: ['api'] })
    type('api')

    fireKey(dom, search(), 'Escape')

    assert.equal(search().value, '')
  })

  test('steps out of an already-empty search box on Escape', () => {
    show({ streams: ['api'] })

    search().focus()
    assert.equal(dom.document.activeElement, search())

    fireKey(dom, search(), 'Escape')

    assert.notEqual(dom.document.activeElement, search())
  })

  test('jumps to the search box on /', () => {
    show({ streams: ['api'] })

    fireKey(dom, dom.document.body, '/')

    assert.equal(dom.document.activeElement, search())
  })

  test('leaves / alone while typing in a field', () => {
    show({ streams: ['api'] })
    type('a')

    const other = dom.document.createElement('input')
    dom.document.body.appendChild(other)
    other.focus()

    fireKey(dom, other, '/')

    assert.equal(dom.document.activeElement, other, 'a / in a text field is just a slash')
  })

  test('leaves a modified / alone', () => {
    show({ streams: ['api'] })

    fireKey(dom, dom.document.body, '/', { metaKey: true })

    assert.notEqual(dom.document.activeElement, search())
  })

  test('reports a new width while the handle is dragged', () => {
    const widths: number[] = []
    show({ onResize: (w) => widths.push(w) })

    const handle = dom.container.querySelector('.resize-handler')!

    firePointer(dom, handle, 'pointerdown')
    firePointer(dom, handle, 'pointermove', { clientX: 320 })

    assert.deepEqual(widths, [320])
  })

  test('ignores pointer movement that is not a drag', () => {
    const widths: number[] = []
    show({ onResize: (w) => widths.push(w) })

    firePointer(dom, dom.container.querySelector('.resize-handler')!, 'pointermove', { clientX: 320 })

    assert.deepEqual(widths, [])
  })

  test('clamps the width to the allowed range', () => {
    const widths: number[] = []
    show({ onResize: (w) => widths.push(w) })

    const handle = dom.container.querySelector('.resize-handler')!

    firePointer(dom, handle, 'pointerdown')
    firePointer(dom, handle, 'pointermove', { clientX: 5000 })
    firePointer(dom, handle, 'pointermove', { clientX: 10 })

    assert.deepEqual(widths, [600, 180])
  })

  test('stops resizing when the pointer is released', () => {
    const widths: number[] = []
    show({ onResize: (w) => widths.push(w) })

    const handle = dom.container.querySelector('.resize-handler')!

    firePointer(dom, handle, 'pointerdown')
    firePointer(dom, handle, 'pointerup')
    firePointer(dom, handle, 'pointermove', { clientX: 320 })

    assert.deepEqual(widths, [])
  })

  test('stops resizing when the gesture is cancelled', () => {
    const widths: number[] = []
    show({ onResize: (w) => widths.push(w) })

    const handle = dom.container.querySelector('.resize-handler')!

    firePointer(dom, handle, 'pointerdown')
    firePointer(dom, handle, 'pointercancel')
    firePointer(dom, handle, 'pointermove', { clientX: 320 })

    assert.deepEqual(widths, [])
  })

  test('marks the body while dragging, so the cursor does not flicker', () => {
    show()

    const handle = dom.container.querySelector('.resize-handler')!

    firePointer(dom, handle, 'pointerdown')
    assert.ok(dom.document.body.classList.contains('resizing'))

    firePointer(dom, handle, 'pointerup')
    assert.equal(dom.document.body.classList.contains('resizing'), false)
  })
})
