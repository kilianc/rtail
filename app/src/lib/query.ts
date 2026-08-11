/*!
 * The filter box's query language.
 *
 * It used to be a bare regexp over the rendered text, which is the wrong tool
 * the moment the lines are JSON: `level.*error` happily matches a payload
 * whose `level` is `info` and whose message three keys away says "error". So
 * the box now parses a small language — terms ANDed together, each one
 * negatable, and field terms that address the parsed payload instead of its
 * text.
 *
 *   payment failed     both words, anywhere in the line
 *   "payment failed"   that phrase
 *   -healthcheck       lines without it
 *   /GET \/1\/users/   an explicit regexp
 *   level:error        the JSON field contains "error"
 *   user.id=42         the field is exactly that
 *   duration>250       numeric comparison
 *   trace_id:*         the field is there at all
 *
 * Matching is case-insensitive until a term contains an uppercase letter (the
 * smart-case convention from ag and ripgrep), so the common case needs no
 * flags and a deliberate `Error` still means `Error`.
 *
 * A field term never matches a line without that field — including `!=`. That
 * makes `status!=200` mean "a status, and not 200"; for "not 200, whether or
 * not there is a status" the negated form `-status:200` is the one to reach
 * for.
 */

import { getPath, parsePath, stringify, type Path } from './json.js'

/** Longest first, so `>=` is never read as `>` followed by a value of `=1`. */
const OPERATORS = ['!=', '>=', '<=', ':', '=', '>', '<'] as const

type Operator = (typeof OPERATORS)[number]

/** A path that can be typed: the same shape the suggestions offer. */
const PATH = /^[A-Za-z_$][\w$-]*(?:\.[\w$-]+|\[\d+\])*$/

/** Flags worth honouring. `g` and `y` carry state between calls; drop them. */
const FLAGS = /[^imsu]/g

export type Needle =
  | { type: 'literal'; value: string; caseSensitive: boolean }
  | { type: 'regexp'; re: RegExp }

interface TextTerm {
  kind: 'text'
  negated: boolean
  needle: Needle
}

interface FieldTerm {
  kind: 'field'
  negated: boolean
  path: Path
  op: Operator
  /** null for `field:*`, which asks only whether the field is there. */
  needle: Needle | null
  /** The right-hand side as a number, parsed once for the comparisons. */
  number: number
}

export type Term = TextTerm | FieldTerm

export interface Query {
  /** Every term has to match. No terms means every line matches. */
  terms: Term[]
  /** What to mark in the matching lines: the positive terms' needles. */
  needles: Needle[]
  /** Set when part of the input did not parse. The rest still filters. */
  error: string | null
  isEmpty: boolean
}

const EMPTY: Query = { terms: [], needles: [], error: null, isEmpty: true }

export function parseQuery(input: string): Query {
  if (!input.trim()) return EMPTY

  const terms: Term[] = []
  let error: string | null = null

  for (const token of tokenize(input)) {
    try {
      const term = parseToken(token)
      if (term) terms.push(term)
    } catch (err) {
      // A half-typed regexp is the normal state of a box being typed into.
      // The terms that did parse keep filtering, and the box says why.
      error ??= (err as Error).message
    }
  }

  // Only the terms whose value is text the line actually contains. The bound
  // in `duration>250` is not something to mark: the payloads that match it are
  // precisely the ones that do not say 250.
  const needles = terms
    .filter((term) => !term.negated && ('text' === term.kind || ':' === term.op || '=' === term.op))
    .map((term) => term.needle)
    .filter((needle): needle is Needle => null !== needle)

  return { terms, needles, error, isEmpty: 0 === terms.length }
}

export function matchesQuery(query: Query, line: { text: string; content: unknown }): boolean {
  for (const term of query.terms) {
    const hit =
      'text' === term.kind
        ? matchNeedle(term.needle, line.text)
        : matchField(term, line.content)

    if (hit === term.negated) return false
  }

  return true
}

export function matchNeedle(needle: Needle, text: string): boolean {
  if ('regexp' === needle.type) return needle.re.test(text)
  if (needle.caseSensitive) return text.includes(needle.value)

  return text.toLowerCase().includes(needle.value.toLowerCase())
}

function matchField(term: FieldTerm, content: unknown): boolean {
  const value = getPath(content, term.path)
  if (undefined === value) return false
  if (null === term.needle) return true

  switch (term.op) {
    case ':':
      return matchNeedle(term.needle, stringify(value))
    case '=':
      return equals(term.needle, value)
    case '!=':
      return !equals(term.needle, value)
    default: {
      const left = toNumber(value)
      if (Number.isNaN(left) || Number.isNaN(term.number)) return false

      if ('>' === term.op) return left > term.number
      if ('>=' === term.op) return left >= term.number
      if ('<' === term.op) return left < term.number
      return left <= term.number
    }
  }
}

function equals(needle: Needle, value: unknown): boolean {
  const text = stringify(value)
  if ('regexp' === needle.type) return needle.re.test(text)
  if (needle.caseSensitive) return text === needle.value

  return text.toLowerCase() === needle.value.toLowerCase()
}

function toNumber(value: unknown): number {
  if ('number' === typeof value) return value
  if ('string' === typeof value && value.trim()) return Number(value)

  return Number.NaN
}

/**
 * Splits on whitespace, except inside quotes and regexps — the two places a
 * space is part of the term rather than the end of it.
 */
function tokenize(input: string): string[] {
  const tokens: string[] = []
  let token = ''
  let quote: string | null = null
  let inRegexp = false

  for (let index = 0; index < input.length; index++) {
    const char = input[index]

    if ('\\' === char && (quote || inRegexp) && index + 1 < input.length) {
      token += char + input[++index]
      continue
    }

    if (quote) {
      if (char === quote) quote = null
      token += char
      continue
    }

    if (inRegexp) {
      if ('/' === char) inRegexp = false
      token += char
      continue
    }

    if ('"' === char || "'" === char) {
      quote = char
      token += char
      continue
    }

    // A slash opens a regexp only where a value can start; anywhere else it is
    // part of a path being searched for, as in `GET /1/users`.
    if ('/' === char && startsValue(token)) {
      inRegexp = true
      token += char
      continue
    }

    if (/\s/.test(char)) {
      if (token) tokens.push(token)
      token = ''
      continue
    }

    token += char
  }

  if (token) tokens.push(token)
  return tokens
}

function startsValue(token: string): boolean {
  return /^[-!]?$/.test(token) || OPERATORS.some((op) => token.endsWith(op))
}

function parseToken(token: string): Term | null {
  const negated = token.startsWith('-') || token.startsWith('!')
  const rest = negated ? token.slice(1) : token
  if (!rest) return null

  const field = splitField(rest)
  if (!field) return { kind: 'text', negated, needle: parseNeedle(rest) }

  const wildcard = ':' === field.op && '*' === field.value
  const needle = wildcard ? null : parseNeedle(field.value)

  return {
    kind: 'field',
    negated,
    path: field.path,
    op: field.op,
    needle,
    number: needle && 'literal' === needle.type ? Number(needle.value) : Number.NaN
  }
}

function splitField(token: string): { path: Path; op: Operator; value: string } | null {
  let found: { index: number; op: Operator } | null = null

  for (const op of OPERATORS) {
    const index = token.indexOf(op)
    if (-1 === index) continue
    if (!found || index < found.index) found = { index, op }
  }

  if (!found || 0 === found.index) return null

  const head = token.slice(0, found.index)
  const value = token.slice(found.index + found.op.length)

  if (!PATH.test(head)) return null

  // `https://example.com` is a URL someone is looking for, not a query about a
  // field called `https`.
  if (':' === found.op && value.startsWith('//')) return null

  const path = parsePath(head)
  return path ? { path, op: found.op, value } : null
}

function parseNeedle(raw: string): Needle {
  const regexp = /^\/(.+)\/([a-z]*)$/.exec(raw)

  if (regexp) {
    const [, source, given] = regexp
    let flags = given.replace(FLAGS, '')

    // Smart case, the regexp spelling of it.
    if (!flags.includes('i') && !/[A-Z]/.test(source)) flags += 'i'

    try {
      return { type: 'regexp', re: new RegExp(source, flags) }
    } catch (err) {
      throw new Error((err as Error).message)
    }
  }

  const value = unquote(raw)
  return { type: 'literal', value, caseSensitive: /[A-Z]/.test(value) }
}

function unquote(raw: string): string {
  const quoted = /^"(.*)"$/.exec(raw) ?? /^'(.*)'$/.exec(raw)
  if (!quoted) return raw

  return quoted[1].replace(/\\(["'\\])/g, '$1')
}
