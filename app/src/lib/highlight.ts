/*!
 * Marking query hits in a line that is already rendered.
 *
 * The line's HTML comes out of the ANSI parser or the syntax highlighter, so
 * the marks cannot be applied with a string replace — that would rewrite tag
 * names and attributes as readily as text. Instead the fragment is parsed and
 * only its text nodes are touched, which also means a mark can never break the
 * surrounding markup.
 *
 * A hit that spans two text nodes — a phrase split across ANSI colour changes,
 * or across the newline of a pretty-printed payload — is not marked. Marking
 * it would mean splitting elements, and the filter has already done the useful
 * part by keeping the line.
 */

import type { Needle } from './query.js'

type Range = [start: number, end: number]

export function highlight(html: string, needles: Needle[]): string {
  if (!html || 0 === needles.length) return html

  const template = document.createElement('template')
  template.innerHTML = html

  const walker = document.createTreeWalker(template.content, NodeFilter.SHOW_TEXT)
  const nodes: Text[] = []
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    nodes.push(node as Text)
  }

  let marked = false

  for (const node of nodes) {
    const text = node.data
    const ranges = findRanges(text, needles)
    if (0 === ranges.length) continue

    const fragment = document.createDocumentFragment()
    let cursor = 0

    for (const [start, end] of ranges) {
      if (start > cursor) fragment.append(text.slice(cursor, start))

      const mark = document.createElement('mark')
      mark.textContent = text.slice(start, end)
      fragment.append(mark)

      cursor = end
    }

    if (cursor < text.length) fragment.append(text.slice(cursor))

    node.replaceWith(fragment)
    marked = true
  }

  return marked ? template.innerHTML : html
}

/** Every hit in the string, sorted and with overlaps merged into one mark. */
function findRanges(text: string, needles: Needle[]): Range[] {
  const ranges: Range[] = []

  for (const needle of needles) {
    if ('literal' === needle.type) {
      if (!needle.value) continue

      const haystack = needle.caseSensitive ? text : text.toLowerCase()
      const target = needle.caseSensitive ? needle.value : needle.value.toLowerCase()

      for (
        let index = haystack.indexOf(target);
        -1 !== index;
        index = haystack.indexOf(target, index + target.length)
      ) {
        ranges.push([index, index + target.length])
      }

      continue
    }

    // A fresh regexp per pass: the query's own instance is shared by every
    // line, and a sticky `lastIndex` would make the marks depend on the order
    // the rows happened to render in.
    const re = new RegExp(needle.re.source, needle.re.flags + 'g')

    for (let match = re.exec(text); match; match = re.exec(text)) {
      if (match[0]) ranges.push([match.index, match.index + match[0].length])
      else re.lastIndex++ // an empty match, from something like /x*/

      if (re.lastIndex > text.length) break
    }
  }

  if (ranges.length < 2) return ranges

  ranges.sort((a, b) => a[0] - b[0])

  const merged: Range[] = [ranges[0]]

  for (const [start, end] of ranges.slice(1)) {
    const last = merged[merged.length - 1]
    if (start <= last[1]) last[1] = Math.max(last[1], end)
    else merged.push([start, end])
  }

  return merged
}
