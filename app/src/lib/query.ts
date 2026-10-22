import { formatCount } from './format.js'
import type { WireLine } from './types.js'

/*!
 * Editing rQL from the UI.
 *
 * Most queries are built by clicking, not typing: expand a row, click a value,
 * get "filter to this" and "exclude this". That only works if adding a term to
 * an existing query is a real operation rather than string concatenation — the
 * bar has to keep showing something the user could have typed themselves, and
 * clicking the same value twice must not produce `a=1 a=1`.
 *
 * This is deliberately a lexer over top-level terms rather than a full parser.
 * The server owns the grammar; the client only needs to add, remove and
 * recognise terms, and a query with parentheses in it is left alone rather
 * than rearranged behind the user's back.
 */

/** One top-level term of a query. */
export interface Term {
  /** The exact source text, so re-rendering is lossless. */
  text: string
  /** Present for `field op value` terms; absent for free text and groups. */
  field?: string
  op?: string
  value?: string
  negated: boolean
}

const OPERATORS = [':~', '>=', '<=', '=', ':', '>', '<']

/**
 * Splits a query into top-level terms.
 *
 * Quoted strings and parenthesised groups stay whole, so a term is never cut
 * in half by a space that happens to be inside one.
 */
export function terms(query: string): Term[] {
  const out: Term[] = []

  let depth = 0
  let quote = ''
  let current = ''

  const flush = () => {
    const text = current.trim()
    current = ''
    if (text) out.push(parseTerm(text))
  }

  for (let i = 0; i < query.length; i++) {
    const char = query[i]

    if (quote) {
      current += char
      if (char === quote && '\\' !== query[i - 1]) quote = ''
      continue
    }

    // Any whitespace separates terms, newlines included, because that is
    // what the server's lexer does — a query written on two lines is two
    // lines of terms, not one term with a line break inside it.
    if (/\s/.test(char)) {
      if (0 === depth) flush()
      else current += char
      continue
    }

    switch (char) {
      case '"':
      case "'":
        quote = char
        current += char
        break

      case '(':
        depth++
        current += char
        break

      case ')':
        depth = Math.max(0, depth - 1)
        current += char
        break

      default:
        current += char
    }
  }

  flush()

  return out
}

function parseTerm(text: string): Term {
  let body = text
  let negated = false

  if (body.startsWith('-') && body.length > 1) {
    negated = true
    body = body.slice(1)
  }

  // A group or a bare keyword is not a comparison.
  if (body.startsWith('(') || /^(AND|OR|NOT)$/i.test(body)) {
    return { text, negated }
  }

  let best = -1
  let bestOp = ''

  for (const op of OPERATORS) {
    const at = body.indexOf(op)
    if (at < 0) continue
    if (best < 0 || at < best || (at === best && op.length > bestOp.length)) {
      best = at
      bestOp = op
    }
  }

  if (best <= 0) return { text, negated }

  return {
    text,
    negated,
    field: body.slice(0, best),
    op: bestOp,
    value: unquote(body.slice(best + bestOp.length))
  }
}

function unquote(value: string): string {
  if (value.length >= 2 && ('"' === value[0] || "'" === value[0]) && value[value.length - 1] === value[0]) {
    return value.slice(1, -1).replace(/\\(.)/g, '$1')
  }
  return value
}

/**
 * Quotes a value if it would not survive being written bare.
 *
 * Deliberately matches the server's own rule, so a query built by clicking and
 * one typed by hand render identically.
 */
export function quote(value: string): string {
  if ('' === value) return '""'
  if (/[\s"'()<>=:-]/.test(value)) {
    return `"${value.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
  }
  return value
}

function render(terms: Term[]): string {
  return terms.map((term) => term.text).join(' ').trim()
}

function termText(field: string, op: string, value: string, negated: boolean): string {
  return `${negated ? '-' : ''}${field}${op}${quote(value)}`
}

/**
 * Adds `field op value`, replacing any existing term for the same field and
 * operator — clicking two values of one field means "show me that one", not
 * "show me rows that are somehow both".
 */
export function withTerm(
  query: string,
  field: string,
  op: string,
  value: string,
  negated = false
): string {
  const kept = terms(query).filter(
    (term) => !(term.field === field && term.op === op && term.negated === negated)
  )

  kept.push({ text: termText(field, op, value, negated), field, op, value, negated })

  return render(kept)
}

/** Removes every term for a field, whatever its operator. */
export function withoutField(query: string, field: string): string {
  return render(terms(query).filter((term) => term.field !== field))
}

/** Reports whether a query already asserts exactly this. */
export function hasTerm(
  query: string,
  field: string,
  op: string,
  value: string,
  negated = false
): boolean {
  return terms(query).some(
    (term) =>
      term.field === field &&
      term.op === op &&
      term.value === value &&
      term.negated === negated
  )
}

/**
 * Toggles a filter: clicking the value you already filtered by removes it.
 *
 * Without this, "filter to this" is a one-way door and the only way back is
 * editing the text by hand.
 */
export function toggleTerm(
  query: string,
  field: string,
  op: string,
  value: string,
  negated = false
): string {
  // Removed by what the term says, not by how it is written: found by value,
  // a bare `region=us-east-1` typed by hand would otherwise be recognised and
  // then not removed, because the click spells it `region="us-east-1"`.
  if (hasTerm(query, field, op, value, negated)) {
    return render(
      terms(query).filter(
        (term) =>
          !(term.field === field && term.op === op && term.value === value && term.negated === negated)
      )
    )
  }
  return withTerm(query, field, op, value, negated)
}

/** The fields a query mentions, for highlighting them in the explorer. */
export function fieldsUsed(query: string): string[] {
  const seen = new Set<string>()

  for (const term of terms(query)) {
    if (term.field) seen.add(term.field)
  }

  return [...seen]
}

/*!
 * Completion.
 */
export interface Completion {
  /** What gets inserted. */
  value: string
  /** What is shown. */
  label: string
  kind: string
  detail?: string
}

/**
 * Suggests completions for the token the cursor is inside.
 *
 * Field names come from the catalog — every key that has actually appeared on
 * this stream — which is what makes the completion real rather than a guess at
 * what a log line might contain.
 */
export function complete(
  query: string,
  cursor: number,
  fields: { name: string; kind: string; occurrences?: number }[]
): { from: number; to: number; items: Completion[] } {
  const before = query.slice(0, cursor)
  // Back to the last whitespace of any kind, or an opening paren — a token
  // starts at the beginning of a line as readily as after a space.
  const start = before.search(/[\s(][^\s(]*$/) + 1
  const token = query.slice(start, cursor)

  // Past an operator the user is typing a value, and we have nothing better to
  // offer than what they already know.
  for (const op of OPERATORS) {
    if (token.includes(op)) return { from: start, to: cursor, items: [] }
  }

  const prefix = token.replace(/^-/, '').toLowerCase()
  const negated = token.startsWith('-')

  const items = fields
    .filter((field) => field.name.toLowerCase().startsWith(prefix))
    .sort((a, b) => (b.occurrences ?? 0) - (a.occurrences ?? 0) || a.name.localeCompare(b.name))
    .slice(0, 12)
    .map((field) => ({
      value: `${negated ? '-' : ''}${field.name}${'envelope' === field.kind ? '' : '='}`,
      label: field.name,
      kind: field.kind,
      // The compact form the explorer and the board both use. This was
      // `17,611×`, which is the same fact at twice the width and in a
      // different notation from the count for the same field three
      // inches to the left.
      detail: field.occurrences ? formatCount(field.occurrences) : undefined
    }))

  return { from: start, to: cursor, items }
}

/*!
 * The substrings a query is looking for, for highlighting in the results.
 *
 * Free text and `k:substring` terms only. An `=` is an exact match on a whole
 * field and marking it inside a message would highlight a coincidence rather
 * than the reason the row matched; a negated term is why a row is *not* here,
 * so marking it would be actively misleading.
 */
export function highlightTerms(query: string): string[] {
  const out: string[] = []

  for (const term of terms(query)) {
    if (term.negated) continue

    if (undefined === term.field) {
      const text = unquote(term.text)
      if (text && !/^(AND|OR|NOT)$/i.test(text) && !text.startsWith('(')) out.push(text)
      continue
    }

    if (':' === term.op && term.value) out.push(term.value)
  }

  return out.filter((term) => term.length > 1)
}

const HTML_ESCAPE = /[&<>"]/

/*!
 * Wraps matches in <mark>, without touching markup.
 *
 * The row body is already HTML — ANSI colouring produces spans — so a naive
 * replace would happily rewrite the inside of a tag and corrupt it. This walks
 * the string and only substitutes in the runs between tags.
 *
 * Matching is over the escaped text, so a term containing one of & < > " will
 * not match the entity it became. That is rare in a search box and the failure
 * is a missing highlight rather than broken markup, which is the right way
 * round.
 *
 * Entities are markup too, and are stepped over the same way tags are. A
 * needle of `lt` or `27` otherwise landed inside `&lt;` or `&#x27;` and split
 * it, and the row showed the pieces of the entity instead of the character.
 */
export function highlight(html: string, needles: string[]): string {
  if (0 === needles.length) return html

  const usable = needles.filter((needle) => !HTML_ESCAPE.test(needle))
  if (0 === usable.length) return html

  const pattern = new RegExp(
    usable.map((needle) => needle.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|'),
    'gi'
  )

  // split() with a capture keeps the entities, at the odd indices.
  const mark = (text: string) =>
    text
      .split(/(&#?\w+;)/)
      .map((part, index) => (index % 2 ? part : part.replace(pattern, '<mark>$&</mark>')))
      .join('')

  let out = ''
  let at = 0

  while (at < html.length) {
    const open = html.indexOf('<', at)

    if (open < 0) {
      out += mark(html.slice(at))
      break
    }

    out += mark(html.slice(at, open))

    const close = html.indexOf('>', open)
    if (close < 0) {
      out += html.slice(open)
      break
    }

    out += html.slice(open, close + 1)
    at = close + 1
  }

  return out
}

/*!
 * Reads a field off a record for display in a column.
 *
 * Envelope first, then promoted root keys, then a dotted path into a nested
 * one — the same precedence the server's resolver and its live predicate use,
 * so a column shows the value the query would have matched on rather than a
 * different one that happens to share a name.
 */
export function columnValue(line: WireLine, field: string): string {
  switch (field) {
    case 'level':
    case 'severity':
      return line.level ?? ''
    case 'msg':
    case 'message':
      return line.msg ?? ''
    case 'stream':
      return line.streamid
    case 'host':
      return line.host
    case 'seq':
      return String(line.seq)
  }

  const fields = line.fields ?? {}

  if (field in fields) return scalar(fields[field])

  const [head, ...rest] = field.split('.')
  if (0 === rest.length || !(head in fields)) return ''

  let current: unknown = fields[head]

  // A promoted nested object arrives as JSON text, not as an object.
  if ('string' === typeof current) {
    try {
      current = JSON.parse(current)
    } catch {
      return ''
    }
  }

  for (const segment of rest) {
    if (null === current || 'object' !== typeof current) return ''
    current = (current as Record<string, unknown>)[segment]
  }

  return scalar(current)
}

function scalar(value: unknown): string {
  if (null === value || undefined === value) return ''
  if ('object' === typeof value) return JSON.stringify(value)
  return String(value)
}

/** One run of query text, classified for colour. */
export interface Token {
  kind: 'field' | 'op' | 'value' | 'string' | 'plain'
  text: string
  /** Offset of this run in the query, so a parse error can be pointed at. */
  at: number
}

/*!
 * Splits a query into coloured runs.
 *
 * A textarea cannot colour its own contents, so the pane draws this list
 * underneath one whose text is transparent. That means the tokeniser's only
 * hard requirement is that it emits every character exactly once, in order —
 * drop one and the ink slides out from under the caret. It is deliberately
 * not the rQL parser: this runs on every keystroke, including on text that is
 * half-typed and does not parse, and "what colour is this word" survives being
 * wrong in a way "what does this query mean" does not.
 */
export function tokenize(query: string): Token[] {
  // The last alternative is the catch-all that keeps that promise: a `!` with
  // no `=` after it matched nothing else, so `wow! level=ERROR` was drawn as
  // `wow level=ERROR` and every column after it sat one off from the caret.
  const pattern = /("(?:[^"\\]|\\.)*"?)|(>=|<=|!=|=|>|<|:)|(\s+)|([^\s"<>=!:]+)|([\s\S])/g
  const tokens: Token[] = []

  // What the last word meant, so the word after an operator reads as a value
  // and the word after a value reads as the next field.
  let afterOp = false

  for (const match of query.matchAll(pattern)) {
    const [text, string, op, space, word, other] = match

    if (undefined !== string) {
      tokens.push({ kind: 'string', text, at: match.index })
      afterOp = false
      continue
    }

    if (undefined !== op) {
      tokens.push({ kind: 'op', text, at: match.index })
      afterOp = true
      continue
    }

    if (undefined !== space || undefined !== other) {
      tokens.push({ kind: 'plain', text, at: match.index })
      continue
    }

    // Boolean glue is punctuation wearing letters, and reads as the operator
    // it is rather than as a field nobody has.
    if (/^(AND|OR|NOT)$/i.test(word)) {
      tokens.push({ kind: 'op', text, at: match.index })
      afterOp = false
      continue
    }

    tokens.push({ kind: afterOp ? 'value' : 'field', text, at: match.index })
    afterOp = false
  }

  return tokens
}
