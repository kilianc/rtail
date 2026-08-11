import test, { afterEach, beforeEach, describe } from 'node:test'
import assert from 'node:assert/strict'
import { TopBar } from '../../../app/src/components/TopBar.tsx'
import type { Prefs } from '../../../app/src/lib/types.ts'
import { act, mount, setupDom, type Dom } from '../../helpers/dom.ts'

const PREFS: Prefs = {
  theme: 'dark',
  fontFamily: 1,
  fontSize: 4,
  ascending: true,
  sidebarWidth: 240,
  favorites: [],
  hiddenTimestamps: [],
  jsonView: 'auto',
  fields: {}
}

describe('TopBar', () => {
  let dom: Dom

  const noop = () => {}

  const base = {
    prefs: PREFS,
    activeStream: 'api' as string | null,
    isFavorite: false,
    paused: false,
    filter: '',
    filterError: null as string | null,
    matched: 0,
    total: 0,
    fields: [] as string[],
    availableFields: [] as Array<{ path: string; count: number }>,
    onChangePrefs: noop as (patch: Partial<Prefs>) => void,
    onToggleFavorite: noop,
    onFilter: noop as (pattern: string) => void,
    onFields: noop as (fields: string[]) => void
  }

  beforeEach(() => {
    dom = setupDom()
  })

  afterEach(() => dom.cleanup())

  const show = (props: Partial<typeof base> = {}) =>
    mount(<TopBar {...base} {...props} />, dom.container)

  const click = (selector: string) =>
    act(() => {
      const el = dom.container.querySelector(selector)
      assert.ok(el, `no element matched ${selector}`)
      ;(el as HTMLElement).click()
    })

  const byLabel = (label: string) =>
    dom.container.querySelector(`[aria-label="${label}"]`) as HTMLButtonElement

  describe('stream controls', () => {
    test('hides the stream controls when nothing is selected', () => {
      show({ activeStream: null })

      assert.equal(dom.container.querySelector('.stream-title'), null)
      assert.equal(dom.container.querySelector('.filter-box'), null)
    })

    test('shows the stream name', () => {
      show({ activeStream: 'api-gateway' })

      assert.match(dom.container.querySelector('.stream-title')!.textContent ?? '', /api-gateway/)
    })

    test('reports the live state', () => {
      show()
      assert.match(dom.container.querySelector('.stream-status')!.textContent ?? '', /Live/)

      show({ paused: true })
      assert.match(dom.container.querySelector('.stream-status')!.textContent ?? '', /Paused/)
    })

    test('marks a favorited stream', () => {
      show({ isFavorite: true })

      assert.match(dom.container.querySelector('.stream-title-favorite')!.className, /on/)
      assert.equal(
        dom.container.querySelector('.stream-title')!.getAttribute('title'),
        'Remove from favorites'
      )
    })

    test('toggles the favorite', () => {
      let toggled = 0
      show({ onToggleFavorite: () => toggled++ })

      click('.stream-title')

      assert.equal(toggled, 1)
    })

    test('reports what is typed into the filter box', () => {
      const typed: string[] = []
      show({ onFilter: (p) => typed.push(p) })

      act(() => {
        const input = dom.container.querySelector('.filter-box input') as HTMLInputElement
        input.value = 'ERROR'
        input.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
      })

      assert.deepEqual(typed, ['ERROR'])
    })
  })

  describe('panels', () => {
    test('opens the info popover', () => {
      show()
      assert.equal(dom.container.querySelector('.popover-info'), null)

      click('.btn-info')

      assert.ok(dom.container.querySelector('.popover-info'))
      assert.equal(byLabel('About rTail').getAttribute('aria-expanded'), 'true')
    })

    test('shows the build version, substituted at bundle time', () => {
      show()
      click('.btn-info')

      assert.match(
        dom.container.querySelector('.version')!.textContent ?? '',
        /^Version \d+\.\d+\.\d+/
      )
    })

    test('closes the info popover when its button is clicked again', () => {
      show()

      click('.btn-info')
      click('.btn-info')

      assert.equal(dom.container.querySelector('.popover-info'), null)
    })

    test('swaps one panel for the other', () => {
      show()

      click('.btn-info')
      click('.btn-settings')

      assert.equal(dom.container.querySelector('.popover-info'), null)
      assert.ok(dom.container.querySelector('.popover-settings'))
    })
  })

  describe('settings', () => {
    /** Opens settings with the given prefs and records every change. */
    function settings(prefs: Partial<Prefs> = {}) {
      const patches: Partial<Prefs>[] = []

      // Unmount first: re-rendering the same component into the same container
      // keeps its state, so a second call would toggle the panel shut instead
      // of opening it fresh.
      mount(null, dom.container)

      show({ prefs: { ...PREFS, ...prefs }, onChangePrefs: (patch) => patches.push(patch) })
      click('.btn-settings')

      return patches
    }

    test('steps the font size down and up', () => {
      const patches = settings({ fontSize: 4 })

      click('.btn-font-smaller')
      click('.btn-font-bigger')

      assert.deepEqual(patches, [{ fontSize: 3 }, { fontSize: 5 }])
    })

    test('resets the font size', () => {
      const patches = settings({ fontSize: 6 })

      click('.btn-font-reset')

      assert.deepEqual(patches, [{ fontSize: 4 }])
    })

    test('marks the reset button when the size is already the default', () => {
      settings({ fontSize: 4 })

      assert.match(dom.container.querySelector('.btn-font-reset')!.className, /selected/)
    })

    test('disables the step buttons at the ends of the range', () => {
      settings({ fontSize: 1 })
      assert.equal(byLabel('Decrease font size').disabled, true)
      assert.equal(byLabel('Increase font size').disabled, false)

      settings({ fontSize: 7 })
      assert.equal(byLabel('Increase font size').disabled, true)
    })

    test('offers every font family and marks the active one', () => {
      settings({ fontFamily: 3 })

      const buttons = dom.container.querySelectorAll('.btn-group.six-grid .btn')

      assert.equal(buttons.length, 6)
      assert.match(dom.container.querySelector('.btn-font-3')!.className, /selected/)
    })

    test('changes the font family', () => {
      const patches = settings()

      click('.btn-font-5')

      assert.deepEqual(patches, [{ fontFamily: 5 }])
    })

    test('changes the sort order', () => {
      const patches = settings({ ascending: true })

      click('.btn-sorting-desc')

      assert.deepEqual(patches, [{ ascending: false }])
      assert.match(dom.container.querySelector('.btn-sorting-asc')!.className, /selected/)
    })

    test('marks the descending button when sorting newest first', () => {
      settings({ ascending: false })

      assert.match(dom.container.querySelector('.btn-sorting-desc')!.className, /selected/)
      assert.doesNotMatch(dom.container.querySelector('.btn-sorting-asc')!.className, /selected/)
    })

    test('changes the theme', () => {
      const patches = settings({ theme: 'dark' })

      click('.btn-theme-light')

      assert.deepEqual(patches, [{ theme: 'light' }])
      assert.match(dom.container.querySelector('.btn-theme-dark')!.className, /selected/)
    })

    test('offers the three json views and marks the active one', () => {
      settings({ jsonView: 'collapsed' })

      const views = [...dom.container.querySelectorAll('.btn-text')].map((el) => el.textContent)

      assert.deepEqual(views, ['Auto', 'One line', 'Pretty'])
      assert.match(
        [...dom.container.querySelectorAll('.btn-text')].find((el) => 'One line' === el.textContent)!
          .className,
        /selected/
      )
    })

    test('changes the json view', () => {
      const patches = settings({ jsonView: 'auto' })

      act(() => {
        const pretty = [...dom.container.querySelectorAll('.btn-text')].find(
          (el) => 'Pretty' === el.textContent
        ) as HTMLElement
        pretty.click()
      })

      assert.deepEqual(patches, [{ jsonView: 'expanded' }])
    })
  })

  describe('search', () => {
    test('passes the match counts down to the box', () => {
      show({ filter: 'boom', matched: 2, total: 9 })

      assert.equal(dom.container.querySelector('.filter-count')?.textContent, '2/9')
    })

    test('surfaces a query that did not parse', () => {
      show({ filter: '/(', filterError: 'Invalid regular expression' })

      assert.match(dom.container.querySelector('.filter-box')!.className, /invalid/)
    })

    test('badges the fields button with this stream\'s field count', () => {
      show({ fields: ['level', 'user.id'] })

      assert.equal(dom.container.querySelector('.btn-fields-count')?.textContent, '2')
    })

    test('opens the syntax card', () => {
      show()

      click('.filter-help')

      assert.ok(dom.container.querySelector('.popover-help'))
      assert.match(dom.container.textContent ?? '', /Filter syntax/)
    })

    test('opens the field picker with what the buffer offers', () => {
      show({ availableFields: [{ path: 'level', count: 4 }] })

      click('.btn-fields')

      assert.ok(dom.container.querySelector('.popover-fields'))
      assert.equal(dom.container.querySelector('.field-path')?.textContent, 'level')
    })

    test('reports a field being picked', () => {
      const picked: string[][] = []
      show({ availableFields: [{ path: 'level', count: 4 }], onFields: (f) => picked.push(f) })

      click('.btn-fields')
      click('.field-row')

      assert.deepEqual(picked, [['level']])
    })

    test('swaps the search panels for the settings panel', () => {
      show()

      click('.filter-help')
      click('.btn-settings')

      assert.equal(dom.container.querySelector('.popover-help'), null)
      assert.ok(dom.container.querySelector('.popover-settings'))
    })
  })
})
