import test, { afterEach, beforeEach, describe } from 'node:test'
import assert from 'node:assert/strict'
import { highlight } from '../../app/src/lib/highlight.ts'
import type { Needle } from '../../app/src/lib/query.ts'
import { setupDom, type Dom } from '../helpers/dom.ts'

const literal = (value: string, caseSensitive = false): Needle => ({
  type: 'literal',
  value,
  caseSensitive
})

const regexp = (re: RegExp): Needle => ({ type: 'regexp', re })

describe('highlight', () => {
  let dom: Dom

  // Parses the fragment rather than doing a string replace, so it needs a DOM.
  beforeEach(() => {
    dom = setupDom()
  })

  afterEach(() => dom.cleanup())

  test('marks a literal hit', () => {
    assert.equal(highlight('a boom here', [literal('boom')]), 'a <mark>boom</mark> here')
  })

  test('marks every occurrence', () => {
    assert.equal(highlight('boom boom', [literal('boom')]), '<mark>boom</mark> <mark>boom</mark>')
  })

  test('folds case for an insensitive needle, keeping the original text', () => {
    assert.equal(highlight('a BOOM here', [literal('boom')]), 'a <mark>BOOM</mark> here')
  })

  test('respects a case-sensitive needle', () => {
    assert.equal(highlight('a BOOM here', [literal('boom', true)]), 'a BOOM here')
  })

  test('returns the html untouched when nothing matches', () => {
    assert.equal(highlight('quiet line', [literal('boom')]), 'quiet line')
  })

  test('returns the html untouched when there are no needles', () => {
    assert.equal(highlight('anything', []), 'anything')
  })

  test('passes empty html straight through', () => {
    assert.equal(highlight('', [literal('boom')]), '')
  })

  test('ignores an empty needle', () => {
    assert.equal(highlight('text', [literal('')]), 'text')
  })

  test('marks inside existing markup without disturbing it', () => {
    const html = '<span class="ansi-red-fg">boom</span> quiet'

    assert.equal(
      highlight(html, [literal('boom')]),
      '<span class="ansi-red-fg"><mark>boom</mark></span> quiet'
    )
  })

  test('never marks tag names or attributes', () => {
    // A string replace would rewrite the class attribute here.
    const html = '<span class="hljs-string">value</span>'

    assert.equal(highlight(html, [literal('span')]), html)
    assert.equal(highlight(html, [literal('hljs')]), html)
  })

  test('leaves a hit that spans two text nodes alone', () => {
    // Marking it would mean splitting elements; the filter has already done
    // the useful part by keeping the line.
    const html = '<span>pay</span><span>ment</span>'

    assert.equal(highlight(html, [literal('payment')]), html)
  })

  test('marks a regexp hit', () => {
    assert.equal(highlight('GET /1/users', [regexp(/\/1\/\w+/)]), 'GET <mark>/1/users</mark>')
  })

  test('marks every regexp hit, not just the first', () => {
    assert.equal(highlight('a1 a2', [regexp(/a\d/)]), '<mark>a1</mark> <mark>a2</mark>')
  })

  test('does not depend on a shared regexp lastIndex', () => {
    // The query's instance is shared across every line; a sticky lastIndex
    // would make the marks depend on the order rows happened to render in.
    const shared = regexp(/boom/g)

    const first = highlight('boom', [shared])
    const second = highlight('boom', [shared])

    assert.equal(first, second)
    assert.equal(second, '<mark>boom</mark>')
  })

  test('terminates on a regexp that can match empty', () => {
    const marked = highlight('abc', [regexp(/x*/)])

    assert.equal(typeof marked, 'string')
  })

  test('merges overlapping hits into one mark', () => {
    const marked = highlight('abcd', [literal('abc'), literal('bcd')])

    assert.equal(marked, '<mark>abcd</mark>')
  })

  test('merges hits that touch into a single mark', () => {
    // Two marks flush against each other would render as one anyway, and the
    // single element is the tidier DOM.
    assert.equal(highlight('ab', [literal('a'), literal('b')]), '<mark>ab</mark>')
  })

  test('keeps hits that do not touch separate', () => {
    assert.equal(highlight('a b', [literal('a'), literal('b')]), '<mark>a</mark> <mark>b</mark>')
  })

  test('escapes nothing it did not already have to', () => {
    // The input html is already escaped by the ANSI parser or the highlighter;
    // marking must not double-escape it.
    assert.equal(highlight('a &amp; b', [literal('&amp;')]), 'a &amp; b')
    assert.equal(highlight('a &amp; b', [literal('a')]), '<mark>a</mark> &amp; b')
  })
})
