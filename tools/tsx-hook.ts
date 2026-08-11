/*!
 * tsx-hook.ts — lets `node --test` import the Preact components.
 *
 * Node strips types from .ts, .mts and .cts on its own, but it does not know
 * what a .tsx file is at all: the ESM loader rejects the extension outright
 * with ERR_UNKNOWN_FILE_EXTENSION, because stripping types is an erasure pass
 * and JSX needs a real transform.
 *
 * So the .tsx files — and only those — go through esbuild on the way in.
 * esbuild is already a devDependency for the webapp bundle, and the JSX
 * settings below are deliberately the same ones tools/build.ts uses, so a
 * component cannot behave differently under test than it does in the browser.
 *
 * The inline source map is what makes `--experimental-test-coverage` report
 * against the original .tsx line numbers instead of the generated output.
 *
 * Test-only: nothing that ships imports this.
 *
 *   node --import ./tools/tsx-hook.ts --test ...
 */

import { readFileSync } from 'node:fs'
import { registerHooks } from 'node:module'
import { fileURLToPath } from 'node:url'
import { transformSync } from 'esbuild'

// TopBar reads __VERSION__, which tools/build.ts substitutes at bundle time.
// Without the same substitution here it would be an undeclared global and the
// info popover would throw the moment a test rendered it.
const pkg = JSON.parse(
  readFileSync(new URL('../package.json', import.meta.url), 'utf8')
) as { version: string }

registerHooks({
  load(url, context, nextLoad) {
    if (!url.endsWith('.tsx')) return nextLoad(url, context)

    const filename = fileURLToPath(url)

    const { code } = transformSync(readFileSync(filename, 'utf8'), {
      loader: 'tsx',
      jsx: 'automatic',
      jsxImportSource: 'preact',
      sourcefile: filename,
      sourcemap: 'inline',
      target: 'es2022',
      format: 'esm',
      define: { __VERSION__: JSON.stringify(pkg.version) }
    })

    return { format: 'module', source: code, shortCircuit: true }
  }
})
