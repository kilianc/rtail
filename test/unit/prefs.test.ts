import test, { afterEach, beforeEach, describe } from 'node:test'
import assert from 'node:assert/strict'
import {
  FONT_FAMILY_COUNT,
  FONT_SIZE_MAX,
  FONT_SIZE_MIN,
  isTheme,
  loadActiveStream,
  loadPrefs,
  MAX_FIELDS,
  savePrefs,
  saveActiveStream
} from '../../app/src/lib/prefs.ts'
import type { Prefs } from '../../app/src/lib/types.ts'
import { setupDom, type Dom } from '../helpers/dom.ts'

const KEY = 'rtail:prefs'

describe('prefs', () => {
  let dom: Dom

  beforeEach(() => {
    dom = setupDom()
  })

  afterEach(() => dom.cleanup())

  const store = (value: unknown) => localStorage.setItem(KEY, JSON.stringify(value))

  test('falls back to defaults when nothing is stored', () => {
    const prefs = loadPrefs()

    assert.deepEqual(prefs, {
      theme: 'dark',
      fontFamily: 1,
      fontSize: 4,
      ascending: true,
      sidebarWidth: 240,
      favorites: [],
      hiddenTimestamps: [],
      jsonView: 'auto',
      fields: {}
    })
  })

  test('survives a corrupt entry', () => {
    // A corrupt entry must not stop the app booting.
    localStorage.setItem(KEY, 'not json {{{')

    assert.equal(loadPrefs().theme, 'dark')
  })

  test('reads a stored preference set back', () => {
    const stored: Prefs = {
      theme: 'light',
      fontFamily: 3,
      fontSize: 6,
      ascending: false,
      sidebarWidth: 320,
      favorites: ['api'],
      hiddenTimestamps: ['worker'],
      jsonView: 'collapsed',
      fields: { api: ['level', 'user.id'] }
    }

    store(stored)

    assert.deepEqual(loadPrefs(), stored)
  })

  test('accepts only the two known themes', () => {
    store({ theme: 'solarized' })

    assert.equal(loadPrefs().theme, 'dark')
  })

  test('clamps the font size to its range', () => {
    store({ fontSize: 99 })
    assert.equal(loadPrefs().fontSize, FONT_SIZE_MAX)

    store({ fontSize: -5 })
    assert.equal(loadPrefs().fontSize, FONT_SIZE_MIN)
  })

  test('clamps the font family to the stacks that exist', () => {
    store({ fontFamily: 99 })

    assert.equal(loadPrefs().fontFamily, FONT_FAMILY_COUNT)
  })

  test('clamps the sidebar width', () => {
    store({ sidebarWidth: 5000 })
    assert.equal(loadPrefs().sidebarWidth, 600)

    store({ sidebarWidth: 10 })
    assert.equal(loadPrefs().sidebarWidth, 180)
  })

  test('falls back when a numeric preference is not a number', () => {
    store({ fontSize: 'huge', sidebarWidth: null })

    assert.equal(loadPrefs().fontSize, 4)
    assert.equal(loadPrefs().sidebarWidth, 240)
  })

  test('keeps a stored false for a boolean preference', () => {
    // `false` is falsy, so a naive default would silently flip it back to true.
    store({ ascending: false })

    assert.equal(loadPrefs().ascending, false)
  })

  test('ignores a non-boolean ascending', () => {
    store({ ascending: 'yes' })

    assert.equal(loadPrefs().ascending, true)
  })

  test('drops non-string entries from the string lists', () => {
    store({ favorites: ['api', 7, null, 'worker'], hiddenTimestamps: [{}, 'ok'] })

    const prefs = loadPrefs()

    assert.deepEqual(prefs.favorites, ['api', 'worker'])
    assert.deepEqual(prefs.hiddenTimestamps, ['ok'])
  })

  test('ignores lists that are not arrays', () => {
    store({ favorites: 'api', hiddenTimestamps: 42 })

    assert.deepEqual(loadPrefs().favorites, [])
    assert.deepEqual(loadPrefs().hiddenTimestamps, [])
  })

  test('accepts only the three json views', () => {
    store({ jsonView: 'collapsed' })
    assert.equal(loadPrefs().jsonView, 'collapsed')

    store({ jsonView: 'sideways' })
    assert.equal(loadPrefs().jsonView, 'auto')
  })

  test('reads the extracted fields, per stream', () => {
    store({ fields: { api: ['level'], worker: ['user.id'] } })

    assert.deepEqual(loadPrefs().fields, { api: ['level'], worker: ['user.id'] })
  })

  test('ignores a fields entry that is not an array', () => {
    store({ fields: { api: 'level', worker: ['ok'] } })

    assert.deepEqual(loadPrefs().fields, { worker: ['ok'] })
  })

  test('drops non-strings and empty entries from the fields', () => {
    store({ fields: { api: [1, null, 'level'], empty: [], gone: [7] } })

    // An entry that filters down to nothing is dropped rather than kept as [].
    assert.deepEqual(loadPrefs().fields, { api: ['level'] })
  })

  test('caps how many fields a stream can extract', () => {
    const tooMany = Array.from({ length: MAX_FIELDS + 5 }, (_, i) => `f${i}`)
    store({ fields: { api: tooMany } })

    assert.equal(loadPrefs().fields.api?.length, MAX_FIELDS)
  })

  test('ignores a fields value that is not an object', () => {
    store({ fields: 'nope' })
    assert.deepEqual(loadPrefs().fields, {})

    store({ fields: null })
    assert.deepEqual(loadPrefs().fields, {})
  })

  test('round-trips through savePrefs', () => {
    const prefs = loadPrefs()
    prefs.theme = 'light'
    prefs.favorites = ['api']

    savePrefs(prefs)

    assert.deepEqual(loadPrefs(), prefs)
  })

  test('does not throw when storage is unavailable', () => {
    // Private browsing, quota, disabled storage.
    const original = dom.window.Storage.prototype.setItem
    dom.window.Storage.prototype.setItem = () => {
      throw new Error('QuotaExceededError')
    }

    try {
      assert.doesNotThrow(() => savePrefs(loadPrefs()))
      assert.doesNotThrow(() => saveActiveStream('api'))
    } finally {
      dom.window.Storage.prototype.setItem = original
    }
  })
})

describe('active stream', () => {
  let dom: Dom

  beforeEach(() => {
    dom = setupDom()
  })

  afterEach(() => dom.cleanup())

  test('is null before anything is chosen', () => {
    assert.equal(loadActiveStream(), null)
  })

  test('round-trips', () => {
    saveActiveStream('api-gateway')

    assert.equal(loadActiveStream(), 'api-gateway')
  })

  test('is cleared by saving null', () => {
    saveActiveStream('api-gateway')
    saveActiveStream(null)

    assert.equal(loadActiveStream(), null)
  })

  test('reads as null when storage throws', () => {
    const original = dom.window.Storage.prototype.getItem
    dom.window.Storage.prototype.getItem = () => {
      throw new Error('SecurityError')
    }

    try {
      assert.equal(loadActiveStream(), null)
    } finally {
      dom.window.Storage.prototype.getItem = original
    }
  })
})

describe('isTheme', () => {
  test('accepts the two known themes', () => {
    assert.equal(isTheme('dark'), true)
    assert.equal(isTheme('light'), true)
  })

  test('rejects anything else', () => {
    assert.equal(isTheme('solarized'), false)
    assert.equal(isTheme(''), false)
  })
})
