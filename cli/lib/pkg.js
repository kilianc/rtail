/*!
 * pkg.js — read package.json from ESM without import attributes.
 *
 * `import ... with { type: 'json' }` is still unstable across the Node
 * versions this CLI supports, so the manifest is read directly.
 */

import { readFileSync } from 'node:fs'

export const pkg = JSON.parse(
  readFileSync(new URL('../../package.json', import.meta.url), 'utf8')
)
