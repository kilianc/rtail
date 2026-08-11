import test, { afterEach, beforeEach, describe } from 'node:test'
import assert from 'node:assert/strict'
import { createRef } from 'preact'
import { FieldsPicker, SearchBar, SearchHelp } from '../../../app/src/components/SearchBar.tsx'
import { MAX_FIELDS } from '../../../app/src/lib/prefs.ts'
import { act, fireKey, mount, setupDom, type Dom } from '../../helpers/dom.ts'

describe('SearchBar', () => {
  let dom: Dom

  const noop = () => {}

  const base = {
    value: '',
    error: null as string | null,
    matched: 0,
    total: 0,
    fieldCount: 0,
    open: null as 'help' | 'fields' | null,
    onChange: noop as (value: string) => void,
    onToggle: noop as (panel: 'help' | 'fields') => void
  }

  beforeEach(() => {
    dom = setupDom()
  })

  afterEach(() => dom.cleanup())

  const show = (props: Partial<typeof base> = {}) =>
    mount(
      <SearchBar
        {...base}
        {...props}
        helpRef={createRef<HTMLButtonElement>()}
        fieldsRef={createRef<HTMLButtonElement>()}
      />,
      dom.container
    )

  const input = () => dom.container.querySelector('.filter-box input') as HTMLInputElement

  function type(value: string) {
    act(() => {
      input().value = value
      input().dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    })
  }

  test('reports what is typed', () => {
    const typed: string[] = []
    show({ onChange: (v) => typed.push(v) })

    type('level:error')

    assert.deepEqual(typed, ['level:error'])
  })

  test('shows no match count until something is typed', () => {
    show({ matched: 0, total: 10 })

    assert.equal(dom.container.querySelector('.filter-count'), null)
  })

  test('shows how many lines matched', () => {
    show({ value: 'boom', matched: 3, total: 10 })

    const count = dom.container.querySelector('.filter-count')

    assert.equal(count?.textContent, '3/10')
    assert.equal(count?.getAttribute('title'), '3 of 10 lines match')
  })

  test('marks the box invalid when the query did not parse', () => {
    show({ value: '/unclosed(', error: 'Invalid regular expression' })

    assert.match(dom.container.querySelector('.filter-box')!.className, /invalid/)
    assert.equal(input().getAttribute('aria-invalid'), 'true')
    assert.equal(input().getAttribute('title'), 'Invalid regular expression')
  })

  test('leaves the box unmarked for a query that parsed', () => {
    show({ value: 'boom' })

    assert.doesNotMatch(dom.container.querySelector('.filter-box')!.className, /invalid/)
    assert.equal(input().getAttribute('aria-invalid'), null)
  })

  test('offers a clear button only once something is typed', () => {
    show()
    assert.equal(dom.container.querySelector('.search-clear'), null)

    show({ value: 'boom' })
    assert.ok(dom.container.querySelector('.search-clear'))
  })

  test('clears the query with the button', () => {
    const typed: string[] = []
    show({ value: 'boom', onChange: (v) => typed.push(v) })

    act(() => {
      ;(dom.container.querySelector('.search-clear') as HTMLElement).click()
    })

    assert.deepEqual(typed, [''])
  })

  test('clears the query on Escape', () => {
    const typed: string[] = []
    show({ value: 'boom', onChange: (v) => typed.push(v) })

    fireKey(dom, input(), 'Escape')

    assert.deepEqual(typed, [''])
  })

  test('steps out of an already-empty box on Escape', () => {
    show({ value: '' })

    input().focus()
    assert.equal(dom.document.activeElement, input())

    fireKey(dom, input(), 'Escape')

    assert.notEqual(dom.document.activeElement, input())
  })

  test('jumps to the filter on f', () => {
    show()

    fireKey(dom, dom.document.body, 'f')

    assert.equal(dom.document.activeElement, input())
  })

  test('leaves f alone while typing in a field', () => {
    show()

    const other = dom.document.createElement('input')
    dom.document.body.appendChild(other)
    other.focus()

    fireKey(dom, other, 'f')

    assert.equal(dom.document.activeElement, other)
  })

  test('leaves a modified f alone', () => {
    show()

    fireKey(dom, dom.document.body, 'f', { metaKey: true })

    assert.notEqual(dom.document.activeElement, input())
  })

  test('toggles the syntax card', () => {
    const toggled: string[] = []
    show({ onToggle: (panel) => toggled.push(panel) })

    act(() => {
      ;(dom.container.querySelector('.filter-help') as HTMLElement).click()
    })

    assert.deepEqual(toggled, ['help'])
  })

  test('toggles the field picker', () => {
    const toggled: string[] = []
    show({ onToggle: (panel) => toggled.push(panel) })

    act(() => {
      ;(dom.container.querySelector('.btn-fields') as HTMLElement).click()
    })

    assert.deepEqual(toggled, ['fields'])
  })

  test('reports which panel is open', () => {
    show({ open: 'help' })
    assert.equal(dom.container.querySelector('.filter-help')!.getAttribute('aria-expanded'), 'true')

    show({ open: 'fields' })
    assert.equal(dom.container.querySelector('.btn-fields')!.getAttribute('aria-expanded'), 'true')
  })

  test('badges the fields button with how many are extracted', () => {
    show({ fieldCount: 0 })
    assert.equal(dom.container.querySelector('.btn-fields-count'), null)
    assert.doesNotMatch(dom.container.querySelector('.btn-fields')!.className, /on/)

    show({ fieldCount: 3 })
    assert.equal(dom.container.querySelector('.btn-fields-count')?.textContent, '3')
    assert.match(dom.container.querySelector('.btn-fields')!.className, /on/)
  })
})

describe('SearchHelp', () => {
  let dom: Dom

  beforeEach(() => {
    dom = setupDom()
  })

  afterEach(() => dom.cleanup())

  test('lists the syntax examples', () => {
    mount(<SearchHelp anchor={dom.document.body} onClose={() => {}} />, dom.container)

    const examples = [...dom.container.querySelectorAll('dt')].map((el) => el.textContent)

    assert.ok(examples.includes('level:error'))
    assert.ok(examples.includes('duration>250'))
    assert.ok(examples.length >= 8, `expected the full card, got ${examples.length} rows`)
  })

  test('closes on Escape', () => {
    let closed = 0
    mount(<SearchHelp anchor={dom.document.body} onClose={() => closed++} />, dom.container)

    fireKey(dom, dom.document, 'Escape')

    assert.equal(closed, 1)
  })
})

describe('FieldsPicker', () => {
  let dom: Dom

  const noop = () => {}

  beforeEach(() => {
    dom = setupDom()
  })

  afterEach(() => dom.cleanup())

  function show({
    fields = [] as string[],
    available = [] as Array<{ path: string; count: number }>,
    onFields = noop as (fields: string[]) => void
  } = {}) {
    mount(
      <FieldsPicker
        anchor={dom.document.body}
        fields={fields}
        available={available}
        onFields={onFields}
        onClose={noop}
      />,
      dom.container
    )
  }

  const rows = () =>
    [...dom.container.querySelectorAll('.field-row')].map((el) => ({
      path: el.querySelector('.field-path')?.textContent,
      count: el.querySelector('.field-count')?.textContent ?? null,
      on: el.classList.contains('on')
    }))

  const draft = () => dom.container.querySelector('.field-input') as HTMLInputElement

  test('says so when the buffer has no JSON yet', () => {
    show()

    assert.match(dom.container.textContent ?? '', /No JSON fields in the buffer yet/)
  })

  test('lists the paths the buffer offers, with counts', () => {
    show({ available: [{ path: 'level', count: 12 }, { path: 'user.id', count: 3 }] })

    assert.deepEqual(rows(), [
      { path: 'level', count: '12', on: false },
      { path: 'user.id', count: '3', on: false }
    ])
  })

  test('puts the selected fields first and marks them', () => {
    // So a field keeps its place as the counts shuffle beneath it.
    show({
      fields: ['user.id'],
      available: [{ path: 'level', count: 12 }, { path: 'user.id', count: 3 }]
    })

    assert.deepEqual(rows().map((row) => row.path), ['user.id', 'level'])
    assert.equal(rows()[0]!.on, true)
    assert.equal(dom.container.querySelector('.field-row')!.getAttribute('aria-pressed'), 'true')
  })

  test('still lists a chosen field whose lines have scrolled out of the buffer', () => {
    show({ fields: ['gone'], available: [] })

    assert.deepEqual(rows(), [{ path: 'gone', count: null, on: true }])
  })

  test('adds a field when one is picked', () => {
    const picked: string[][] = []
    show({ available: [{ path: 'level', count: 1 }], onFields: (f) => picked.push(f) })

    act(() => {
      ;(dom.container.querySelector('.field-row') as HTMLElement).click()
    })

    assert.deepEqual(picked, [['level']])
  })

  test('removes a field when one is unpicked', () => {
    const picked: string[][] = []
    show({
      fields: ['level', 'user.id'],
      available: [{ path: 'level', count: 1 }],
      onFields: (f) => picked.push(f)
    })

    act(() => {
      ;(dom.container.querySelector('.field-row') as HTMLElement).click()
    })

    assert.deepEqual(picked, [['user.id']])
  })

  test('refuses to add beyond the cap', () => {
    const full = Array.from({ length: MAX_FIELDS }, (_, i) => `f${i}`)
    const picked: string[][] = []

    show({ fields: full, available: [{ path: 'extra', count: 1 }], onFields: (f) => picked.push(f) })

    act(() => {
      const extra = [...dom.container.querySelectorAll('.field-row')].at(-1) as HTMLElement
      extra.click()
    })

    assert.deepEqual(picked, [], `expected the ${MAX_FIELDS}-field cap to hold`)
  })

  test('adds a hand-typed path', () => {
    const picked: string[][] = []
    show({ onFields: (f) => picked.push(f) })

    act(() => {
      draft().value = 'req.headers.host'
      draft().dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    })

    act(() => {
      dom.container.querySelector('form')!.dispatchEvent(
        new dom.window.Event('submit', { bubbles: true, cancelable: true })
      )
    })

    assert.deepEqual(picked, [['req.headers.host']])
    assert.equal(draft().value, '', 'the box should reset after adding')
  })

  test('ignores a hand-typed path that is not a valid path', () => {
    const picked: string[][] = []
    show({ onFields: (f) => picked.push(f) })

    act(() => {
      draft().value = '...'
      draft().dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    })

    act(() => {
      dom.container.querySelector('form')!.dispatchEvent(
        new dom.window.Event('submit', { bubbles: true, cancelable: true })
      )
    })

    assert.deepEqual(picked, [])
  })

  test('ignores an empty or duplicate hand-typed path', () => {
    const picked: string[][] = []
    show({ fields: ['level'], onFields: (f) => picked.push(f) })

    const submit = (value: string) => {
      act(() => {
        draft().value = value
        draft().dispatchEvent(new dom.window.Event('input', { bubbles: true }))
      })
      act(() => {
        dom.container.querySelector('form')!.dispatchEvent(
          new dom.window.Event('submit', { bubbles: true, cancelable: true })
        )
      })
    }

    submit('   ')
    submit('level')

    assert.deepEqual(picked, [])
  })

  test('offers a reset only once something is extracted', () => {
    show()
    assert.equal(dom.container.querySelector('.field-clear'), null)

    show({ fields: ['level'] })
    assert.ok(dom.container.querySelector('.field-clear'))
  })

  test('clears every field with the reset', () => {
    const picked: string[][] = []
    show({ fields: ['level'], onFields: (f) => picked.push(f) })

    act(() => {
      ;(dom.container.querySelector('.field-clear') as HTMLElement).click()
    })

    assert.deepEqual(picked, [[]])
  })
})
