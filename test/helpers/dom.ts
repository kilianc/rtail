/*!
 * dom.ts — a DOM for the component tests.
 *
 * The components are rendered against jsdom rather than a real browser: the
 * browser's job is covered by the Playwright suite in test/e2e, and these
 * tests are here for the branches that are awkward to reach through a UI.
 */

import { JSDOM } from 'jsdom'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Everything the components (and preact itself) reach for off the global.
const GLOBALS = [
  'window', 'document', 'navigator', 'localStorage', 'sessionStorage',
  'HTMLElement', 'HTMLInputElement', 'Element', 'Node', 'Event', 'CustomEvent',
  'MouseEvent', 'KeyboardEvent', 'getComputedStyle', 'requestAnimationFrame',
  'cancelAnimationFrame',
  // Used by highlight.ts, which walks the rendered fragment rather than
  // string-replacing into it.
  'NodeFilter', 'DocumentFragment', 'Text'
] as const

export interface Dom {
  window: JSDOM['window']
  document: Document
  /** A detached-but-attached container that each render targets. */
  container: HTMLElement
  cleanup(): void
}

/**
 * Installs a fresh jsdom on the globals and returns a mount point.
 *
 * Each test gets its own document, so localStorage and document.body classes
 * cannot leak from one case into the next.
 */
export function setupDom(url = 'http://localhost/'): Dom {
  const dom = new JSDOM('<!doctype html><html><body></body></html>', {
    url,
    pretendToBeVisual: true
  })

  const win = dom.window as unknown as Record<string, unknown>
  const previous = new Map<string, PropertyDescriptor | undefined>()

  // Assignment is not enough: Node defines some of these (`navigator`) as
  // getter-only accessors on globalThis, so writing to them throws. Swap the
  // descriptors instead, and put the originals back on cleanup.
  for (const key of GLOBALS) {
    previous.set(key, Object.getOwnPropertyDescriptor(globalThis, key))
    Object.defineProperty(globalThis, key, {
      value: win[key],
      configurable: true,
      writable: true
    })
  }

  // jsdom implements neither pointer capture nor PointerEvent. The sidebar's
  // resize handle calls setPointerCapture unconditionally, so stub it rather
  // than branch the component around a test-only concern.
  const proto = dom.window.HTMLElement.prototype as unknown as Record<string, unknown>
  proto.setPointerCapture = () => {}
  proto.releasePointerCapture = () => {}

  // Preact decides how to register an `onFooBar` prop by asking the element
  // whether it has a matching `onfoobar` IDL attribute: if it does, the
  // listener is registered lowercase ('pointerdown'), and if it does not, the
  // prop name is used as-is ('PointerDown'). jsdom ships no pointer-event IDL
  // attributes, so without these a component's onPointerDown would silently
  // listen for an event no browser ever fires.
  for (const name of ['pointerdown', 'pointermove', 'pointerup', 'pointercancel']) {
    if (`on${name}` in proto) continue
    Object.defineProperty(proto, `on${name}`, { value: null, writable: true, configurable: true })
  }

  const container = dom.window.document.createElement('div')
  dom.window.document.body.appendChild(container)

  return {
    window: dom.window,
    document: dom.window.document,
    container,
    cleanup() {
      // Inside act(), so the unmount effects actually run before the globals
      // are pulled out from under them. Preact queues effect cleanups rather
      // than running them inline, and App closes its socket in one — skip the
      // flush and the socket survives the test, ping timer and all, holding
      // the process open long after the assertions have passed.
      act(() => {
        render(null, container)
      })

      container.remove()

      for (const [key, descriptor] of previous) {
        if (descriptor) Object.defineProperty(globalThis, key, descriptor)
        else delete (globalThis as unknown as Record<string, unknown>)[key]
      }

      dom.window.close()
    }
  }
}

/** Renders a vnode into the container, flushing effects. */
export function mount(vnode: unknown, container: HTMLElement): void {
  act(() => {
    render(vnode as never, container)
  })
}

/**
 * Dispatches a pointer event.
 *
 * jsdom has no PointerEvent constructor, but the handlers only read `target`,
 * `clientX` and `pointerId` — all of which a MouseEvent carries or can be
 * given — and preact listens by event name, so a MouseEvent named
 * 'pointerdown' reaches the handler exactly like the real thing.
 */
export function firePointer(
  dom: Dom,
  el: Element,
  type: string,
  init: { clientX?: number; pointerId?: number } = {}
): void {
  const event = new dom.window.MouseEvent(type, {
    bubbles: true,
    cancelable: true,
    clientX: init.clientX ?? 0
  })

  Object.defineProperty(event, 'pointerId', { value: init.pointerId ?? 1 })

  act(() => {
    el.dispatchEvent(event)
  })
}

export function fireKey(
  dom: Dom,
  target: EventTarget,
  key: string,
  init: KeyboardEventInit = {}
): void {
  act(() => {
    target.dispatchEvent(
      new dom.window.KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init })
    )
  })
}

export { act }
