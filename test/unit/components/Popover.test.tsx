import test, { afterEach, beforeEach, describe } from 'node:test'
import assert from 'node:assert/strict'
import { Popover } from '../../../app/src/components/Popover.tsx'
import { act, fireKey, firePointer, mount, setupDom, type Dom } from '../../helpers/dom.ts'

describe('Popover', () => {
  let dom: Dom

  beforeEach(() => {
    dom = setupDom()
  })

  afterEach(() => dom.cleanup())

  /** Renders a popover anchored to a button that is in the document. */
  function open(onClose = () => {}) {
    const anchor = dom.document.createElement('button')
    dom.document.body.appendChild(anchor)

    mount(
      <Popover anchor={anchor} onClose={onClose} class="popover-test">
        <span class="child">content</span>
      </Popover>,
      dom.container
    )

    return anchor
  }

  test('renders its children inside the content panel', () => {
    open()

    const content = dom.container.querySelector('.popover-content')

    assert.ok(content, 'expected a .popover-content panel')
    assert.match(content.className, /popover-test/)
    assert.equal(content.querySelector('.child')?.textContent, 'content')
  })

  test('becomes visible once it has measured itself', () => {
    open()

    // jsdom reports zero for every box, but the placement effect still runs —
    // what matters is that it committed a position and dropped the
    // visibility:hidden that prevents a flash at 0,0 on the first paint.
    const panel = dom.container.querySelector('.popover') as HTMLElement

    assert.equal(panel.style.visibility, 'visible')
    assert.equal(panel.style.top, '0px')
  })

  test('closes on Escape', () => {
    let closed = 0
    open(() => closed++)

    fireKey(dom, dom.document, 'Escape')

    assert.equal(closed, 1)
  })

  test('ignores other keys', () => {
    let closed = 0
    open(() => closed++)

    fireKey(dom, dom.document, 'Enter')

    assert.equal(closed, 0)
  })

  test('closes on a pointer press outside itself', () => {
    let closed = 0
    open(() => closed++)

    firePointer(dom, dom.document.body, 'pointerdown')

    assert.equal(closed, 1)
  })

  test('stays open when the press lands inside the panel', () => {
    let closed = 0
    open(() => closed++)

    const child = dom.container.querySelector('.child')!
    firePointer(dom, child, 'pointerdown')

    assert.equal(closed, 0)
  })

  test('stays open when the press lands on its own anchor', () => {
    let closed = 0
    const anchor = open(() => closed++)

    // The anchor is the button that opened it; letting the outside-click
    // handler fire here would close and immediately reopen the panel.
    firePointer(dom, anchor, 'pointerdown')

    assert.equal(closed, 0)
  })

  test('repositions on a window resize', () => {
    open()

    act(() => {
      dom.window.dispatchEvent(new dom.window.Event('resize'))
    })

    assert.ok(dom.container.querySelector('.popover'), 'expected the panel to survive a resize')
  })

  test('detaches its listeners on unmount', () => {
    let closed = 0
    open(() => closed++)

    mount(null, dom.container)
    fireKey(dom, dom.document, 'Escape')

    assert.equal(closed, 0, 'expected no callback after unmount')
  })
})
