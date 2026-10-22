/*!
 * build.js — the whole webapp build.
 *
 *   node tools/build.js            build into app/ for local development
 *   node tools/build.js --watch    ... and rebuild on change
 *   node tools/build.js --dist     minified, into web/dist/ for the Go binary
 *
 * Replaces the gulp pipeline (gulp 3 cannot run on Node >= 12) with the
 * esbuild API directly.
 */

import { cp, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { watch } from 'node:fs'
import { fileURLToPath } from 'node:url'
import * as esbuild from 'esbuild'

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

/*!
 * The stylesheets, concatenated in cascade order.
 *
 * This was dart-sass. What it was actually being asked to do, by the end, was
 * resolve four `@use` lines and strip `//` comments: the source had no
 * `@extend`, no `@function`, no `@if`, no `math.*`, and its nesting, its
 * custom properties and its `@layer` blocks are all plain CSS now. So the
 * stylesheets are plain CSS, and this is the whole build.
 *
 * Order matters and is the order Sass emitted. The four partials are
 * unlayered, which puts them above every `@layer` in main.css — the same
 * cascade the `@use` lines produced, kept deliberately rather than by
 * accident of file order.
 *
 * `url()` paths are left exactly as written. They are relative to the output
 * file, not the source, and both sit one level under app/ — so `../images/x`
 * resolves from app/styles/ while editing and from app/css/ once built.
 */
const SHEETS = ['tokens', 'fonts', 'ansi', 'highlightjs', 'main']

async function buildCss() {
  const parts = await Promise.all(
    SHEETS.map((name) => readFile(path(`app/styles/${name}.css`), 'utf8'))
  )

  const source = parts.join('\n')

  // esbuild's CSS minifier, on a string: no bundling, so nothing resolves or
  // rewrites a url() behind our back.
  const css = isDist
    ? (await esbuild.transform(source, { loader: 'css', minify: true })).code
    : source

  await mkdir(`${outDir}/css`, { recursive: true })
  await writeFile(`${outDir}/css/main.css`, css)
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

  // The stylesheet directory is flat, so a plain non-recursive directory
  // watch covers every sheet — and watching the directory rather than each
  // file means edits that replace a file (most editors save atomically via
  // rename) still register.
  let pending = null
  watch(path('app/styles'), () => {
    clearTimeout(pending)
    pending = setTimeout(async () => {
      try {
        await buildCss()
        console.log('css rebuilt')
      } catch (err) {
        console.error('css error:', err.message)
      }
    }, 50)
  })

  console.log('watching for changes ...')
}
