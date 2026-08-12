/*!
 * build-site.ts — the rtail.ciuffolo.com landing page.
 *
 *   node tools/build-site.ts            build into site/dist
 *   node tools/build-site.ts --watch    ... and rebuild on change
 *
 * This is what Vercel runs. It is deliberately separate from tools/build.ts:
 * that one builds the product, this one builds the page that advertises it.
 *
 * The preview embedded on the page is not a screenshot or a mock-up — it is
 * app/src, bundled unmodified, with one module swapped. `site/src/connection.ts`
 * takes the place of the app's socket.io transport and feeds it a synthetic
 * stream, because rtail ingests over UDP and holds a socket open, neither of
 * which a static deployment can do. Everything else on screen is the real
 * component tree and the real stylesheet, so the preview cannot drift from the
 * product: if the app changes, the page changes with it.
 */

import { cp, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { watch } from 'node:fs'
import { fileURLToPath } from 'node:url'
import * as esbuild from 'esbuild'
import * as sass from 'sass'

const root = new URL('../', import.meta.url)
const path = (rel: string): string => fileURLToPath(new URL(rel, root))

const args = new Set(process.argv.slice(2))
const isWatch = args.has('--watch')

const outDir = path('site/dist')
const pkg = JSON.parse(await readFile(path('package.json'), 'utf8')) as { version: string }

/**
 * Swaps the app's socket.io transport for the synthetic one.
 *
 * Resolution-level rather than a source-level flag, so app/src carries no
 * knowledge that a demo build exists.
 */
const demoTransport: esbuild.Plugin = {
  name: 'demo-transport',
  setup(build) {
    build.onResolve({ filter: /(^|\/)lib\/connection\.ts$/ }, () => ({
      path: path('site/src/connection.ts')
    }))
  }
}

/**
 * Two stylesheets: the page's own, and the app's — the preview needs the real
 * one, compiled from the same source the product ships.
 */
async function buildCss(): Promise<string[]> {
  await mkdir(`${outDir}/css`, { recursive: true })

  const site = sass.compile(path('site/scss/site.scss'), {
    style: 'compressed',
    quietDeps: true
  })
  await writeFile(`${outDir}/css/site.css`, site.css)

  const app = sass.compile(path('app/scss/main.scss'), {
    style: 'compressed',
    quietDeps: true
  })
  await writeFile(`${outDir}/css/main.css`, app.css)

  return [...site.loadedUrls, ...app.loadedUrls]
    .filter((url) => 'file:' === url.protocol)
    .map((url) => fileURLToPath(url))
}

const jsOptions: esbuild.BuildOptions = {
  // preview.js is the product; site.js is the page's own handful of behaviours.
  entryPoints: {
    preview: path('app/src/main.tsx'),
    site: path('site/src/site.ts')
  },
  outdir: outDir,
  bundle: true,
  format: 'esm',
  target: ['es2022'],
  jsx: 'automatic',
  jsxImportSource: 'preact',
  minify: true,
  sourcemap: false,
  legalComments: 'none',
  define: { __VERSION__: JSON.stringify(pkg.version) },
  plugins: [demoTransport],
  logLevel: 'warning'
}

async function buildStatic(): Promise<void> {
  await cp(path('site/index.html'), `${outDir}/index.html`)
  await cp(path('site/preview.html'), `${outDir}/preview.html`)
  await cp(path('site/robots.txt'), `${outDir}/robots.txt`)
  // The preview renders the app's icons, and the page reuses its favicons.
  await cp(path('app/images'), `${outDir}/images`, { recursive: true })
}

await rm(outDir, { recursive: true, force: true })
await buildCss()
await esbuild.build(jsOptions)
await buildStatic()

console.log('built site/dist')

if (isWatch) {
  const context = await esbuild.context(jsOptions)
  await context.watch()

  const rebuild = async (label: string, fn: () => Promise<unknown>): Promise<void> => {
    try {
      await fn()
      console.log(`${label} rebuilt`)
    } catch (err) {
      console.error(`${label} error:`, (err as Error).message)
    }
  }

  const debounce = (fn: () => void): (() => void) => {
    let pending: NodeJS.Timeout | undefined
    return () => {
      clearTimeout(pending)
      pending = setTimeout(fn, 50)
    }
  }

  const onCss = debounce(() => void rebuild('css', buildCss))
  const onHtml = debounce(() => void rebuild('html', buildStatic))

  watch(path('site/scss'), onCss)
  watch(path('app/scss'), onCss)

  // Non-recursive, and filtered to the files actually copied — the output
  // directory is a child of site/, and rebuilding on our own writes would loop.
  watch(path('site'), (_event, filename) => {
    if (filename && /\.(html|txt)$/.test(filename)) onHtml()
  })

  console.log('watching for changes ...')
}
