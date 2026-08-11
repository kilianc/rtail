/*!
 * JSON field paths.
 *
 * Both halves of the JSON work need the same primitive: a dotted path, parsed
 * once, read out of a payload. `level`, `user.id`, `items[0].sku`.
 *
 * Only paths that can be typed back into the filter box are ever produced —
 * a key with a dot or a space in it cannot be addressed, so it is left out of
 * the suggestions rather than offered and then not working.
 */

export type Path = Array<string | number>

/** A path seen in the buffer, and how many lines carry it. */
export interface FieldPath {
  path: string
  count: number
}

/** Depth beyond which nested objects are shown whole rather than descended. */
const MAX_DEPTH = 4

const KEY = /^[A-Za-z_$][\w$-]*$/

/** Parses `items[0].sku` into `['items', 0, 'sku']`, or null if malformed. */
export function parsePath(input: string): Path | null {
  const path: Path = []
  let segment = ''
  let closed = false

  for (let index = 0; index < input.length; index++) {
    const char = input[index]

    if ('.' === char) {
      if (segment) path.push(segment)
      else if (!closed) return null
      segment = ''
      closed = false
      continue
    }

    if ('[' === char) {
      const end = input.indexOf(']', index)
      if (-1 === end) return null

      const subscript = input.slice(index + 1, end)
      if (!/^\d+$/.test(subscript)) return null

      if (segment) path.push(segment)
      path.push(Number(subscript))
      segment = ''
      closed = true
      index = end
      continue
    }

    if (closed) return null
    segment += char
  }

  if (segment) path.push(segment)
  return path.length ? path : null
}

/**
 * Reads a path out of a payload, or undefined if any hop is missing.
 *
 * Own properties only: `__proto__.constructor` is a path a user can type, and
 * walking into the prototype chain would answer it with something that is not
 * in their log line.
 */
export function getPath(value: unknown, path: Path): unknown {
  let current = value

  for (const key of path) {
    if (null === current || 'object' !== typeof current) return undefined

    if ('number' === typeof key) {
      if (!Array.isArray(current)) return undefined
      current = current[key]
    } else {
      if (!Object.prototype.hasOwnProperty.call(current, key)) return undefined
      current = (current as Record<string, unknown>)[key]
    }

    if (undefined === current) return undefined
  }

  return current
}

/**
 * The paths present in a set of payloads, most common first.
 *
 * Arrays count as leaves: `items` is a field you can extract, `items[0].sku`
 * is one you have to type. Suggesting a path per array element would bury the
 * fields that actually repeat across lines.
 */
export function collectPaths(contents: unknown[], limit = 40): FieldPath[] {
  const counts = new Map<string, number>()

  const walk = (value: unknown, prefix: string, depth: number) => {
    for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
      if (!KEY.test(key)) continue

      const label = prefix ? `${prefix}.${key}` : key

      if (isPlainObject(child) && depth < MAX_DEPTH) walk(child, label, depth + 1)
      else counts.set(label, (counts.get(label) ?? 0) + 1)
    }
  }

  for (const content of contents) {
    if (isPlainObject(content)) walk(content, '', 1)
  }

  return [...counts]
    .map(([path, count]) => ({ path, count }))
    .sort((a, b) => b.count - a.count || a.path.localeCompare(b.path))
    .slice(0, limit)
}

export function isPlainObject(value: unknown): value is Record<string, unknown> {
  return null !== value && 'object' === typeof value && !Array.isArray(value)
}

/** How a value reads inside a query or a `key=value` pair. */
export function stringify(value: unknown): string {
  if ('string' === typeof value) return value
  if (null === value) return 'null'

  if ('object' === typeof value) {
    try {
      return JSON.stringify(value) ?? ''
    } catch {
      // Cyclic payloads cannot arrive over the wire, but a getter that throws
      // is not worth taking the viewport down for.
      return ''
    }
  }

  return String(value)
}
