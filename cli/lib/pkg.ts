/*!
 * pkg.ts — read package.json from ESM without import attributes.
 *
 * `import ... with { type: 'json' }` would work on the Node versions this CLI
 * now supports, but it pins the manifest into the module graph at parse time,
 * which the test suite has no way to stub. A plain read stays swappable.
 */

import { readFileSync } from 'node:fs'

export interface Pkg {
  name: string
  version: string
  [key: string]: unknown
}

export const pkg = JSON.parse(
  readFileSync(new URL('../../package.json', import.meta.url), 'utf8')
) as Pkg
