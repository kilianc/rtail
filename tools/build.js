/*!
 * build.js — the whole webapp build.
 *
 *   node tools/build.js            build into app/ for local development
 *   node tools/build.js --watch    ... and rebuild on change
 *   node tools/build.js --dist     minified, into web/dist/ for the Go binary
 *
 * Replaces the gulp pipeline (gulp 3 cannot run on Node >= 12) with the
 * esbuild and dart-sass APIs directly.
 */

import { cp, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { watch } from 'node:fs'
import { fileURLToPath } from 'node:url'
import * as esbuild from 'esbuild'
import * as sass from 'sass'

const root = new URL('../', import.meta.url)
const path = (rel) => fileURLToPath(new URL(rel, root))

const args = new Set(process.argv.slice(2))
const isDist = args.has('--dist')
const isWatch = args.has('--watch')

// The distribution build lands in web/dist because that is what the Go binary
// embeds — see web/web.go. Development builds stay in app/, which the server
// serves straight from disk via --web-root.
const outDir = isDist ? path('web/dist') : path('app')
const pkg = JSON.parse(await readFile(path('package.json'), 'utf8'))

/**
 * Compile the stylesheets.
 */
async function buildCss() {
  const result = sass.compile(path('app/scss/main.scss'), {
    style: isDist ? 'compressed' : 'expanded',
    sourceMap: !isDist,
    quietDeps: true
  })

  await mkdir(`${outDir}/css`, { recursive: true })
  await writeFile(`${outDir}/css/main.css`, result.css)

  return result.loadedUrls
    .filter((url) => 'file:' === url.protocol)
    .map((url) => fileURLToPath(url))
}

/**
 * Bundle the app. Shared config so watch and one-shot builds cannot drift.
 */
const jsOptions = {
  entryPoints: [path('app/src/main.tsx')],
  outfile: `${outDir}/bundle.js`,
  bundle: true,
  format: 'esm',
  target: ['es2022'],
  jsx: 'automatic',
  jsxImportSource: 'preact',
  minify: isDist,
  sourcemap: !isDist,
  legalComments: 'none',
  define: { __VERSION__: JSON.stringify(pkg.version) },
  logLevel: 'warning'
}

async function buildStatic() {
  await cp(path('app/index.html'), `${outDir}/index.html`)
  await cp(path('app/images'), `${outDir}/images`, { recursive: true })

  // web/dist is wiped on every dist build, but the directory itself has to
  // survive in git: `go:embed all:dist` fails to compile if it is missing, and
  // a Go build must not depend on having run the Node toolchain first.
  await writeFile(`${outDir}/.gitkeep`, '')
}

if (isDist) {
  await rm(outDir, { recursive: true, force: true })
}

await buildCss()
await esbuild.build(jsOptions)

if (isDist) {
  await buildStatic()
  console.log('built web/dist/ — rebuild the server to embed it')
} else {
  console.log('built app/bundle.js and app/css/main.css')
}

if (isWatch) {
  const context = await esbuild.context(jsOptions)
  await context.watch()

  // dart-sass has no watch API. The stylesheet directory is flat, so a plain
  // non-recursive directory watch covers every partial — and watching the
  // directory rather than each file means edits that replace a file (most
  // editors save atomically via rename) still register.
  let pending = null
  watch(path('app/scss'), () => {
    clearTimeout(pending)
    pending = setTimeout(async () => {
      try {
        await buildCss()
        console.log('css rebuilt')
      } catch (err) {
        console.error('sass error:', err.message)
      }
    }, 50)
  })

  console.log('watching for changes ...')
}
