/*!
 * build-site.js — the rtail.ciuffolo.com landing page.
 *
 *   node tools/build-site.js            build into site/dist
 *   node tools/build-site.js --watch    ... and rebuild on change
 *
 * This is what Vercel runs. It is deliberately separate from tools/build.js:
 * that one builds the product, this one builds the page that advertises it.
 *
 * The page is static HTML and one stylesheet. It carries a drawing of the
 * explorer rather than the explorer itself, because the app is a query client
 * for a Go server that holds a stream open and reads Parquet — none of which a
 * static deployment can do. The drawing is dimensioned by the app's own tokens,
 * which this build puts in front of the page's sheet, so the greys, the blue,
 * the severity colours, the row height and the gutter cannot drift from the
 * product even though the markup is the page's own.
 */

import { cp, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { watch } from 'node:fs'
import { fileURLToPath } from 'node:url'
import * as esbuild from 'esbuild'

const root = new URL('../', import.meta.url)
const path = (rel) => fileURLToPath(new URL(rel, root))

const args = new Set(process.argv.slice(2))
const isWatch = args.has('--watch')

const outDir = path('site/dist')

// Everything in site/ that ships as-is. The stylesheet is compiled, the rest
// is copied, and nothing is bundled — see site/site.js for why.
const statics = ['index.html', 'docs.html', 'site.js', 'robots.txt']

// The page's sheet, behind the app's tokens. Concatenated rather than
// `@import`ed, the same way tools/build.js assembles the webapp's stylesheet:
// one request, and no question about which palette won.
const SHEETS = [path('app/styles/tokens.css'), path('site/styles/site.css')]

/**
 * Build the stylesheet.
 */
async function buildCss() {
  const parts = await Promise.all(SHEETS.map((file) => readFile(file, 'utf8')))

  // esbuild's CSS minifier, on a string: no bundling, so nothing resolves or
  // rewrites a url() behind our back.
  const { code } = await esbuild.transform(parts.join('\n'), {
    loader: 'css',
    minify: true
  })

  await mkdir(`${outDir}/css`, { recursive: true })
  await writeFile(`${outDir}/css/site.css`, code)
}

async function buildStatic() {
  await mkdir(outDir, { recursive: true })

  for (const file of statics) {
    await cp(path(`site/${file}`), `${outDir}/${file}`)
  }

  // The favicons and the wordmark are the app's, not a second set drawn for
  // the page: a tab showing the site and a tab showing a server should carry
  // the same icon.
  await cp(path('app/images/common'), `${outDir}/images/common`, { recursive: true })
}

await rm(outDir, { recursive: true, force: true })
await buildCss()
await buildStatic()

console.log('built site/dist/')

if (isWatch) {
  // One timer per target. They shared one, so an html change inside the 50ms
  // window cancelled a css rebuild already queued, and the stylesheet stayed
  // stale until something else touched it.
  const pending = new Map()

  const rebuild = (what, run) => {
    clearTimeout(pending.get(what))
    pending.set(what, setTimeout(async () => {
      pending.delete(what)

      try {
        await run()
        console.log(`${what} rebuilt`)
      } catch (err) {
        console.error(`${what} error:`, err.message)
      }
    }, 50))
  }

  // Directory watches rather than file watches: most editors save atomically
  // by renaming, which detaches a watch on the file itself.
  watch(path('site/styles'), () => rebuild('css', buildCss))
  watch(path('app/styles'), () => rebuild('css', buildCss))
  watch(path('site'), () => rebuild('html', buildStatic))

  console.log('watching for changes ...')
}
