# rtail.ciuffolo.com

The landing page for v2. It is deliberately not part of the product build —
`npm run build` builds the webapp, `npm run build:site` builds this.

    make site         # build into site/dist
    make site-serve   # build it and serve it locally

Two pages, one stylesheet, sixty lines of JavaScript. `site/site.js` is served
as written rather than bundled: the webapp is TypeScript and goes through
esbuild, but DOM wiring this small does not need a build step, and not having
one means the file you read is the file the browser runs.

## The page follows the app, not a second design

`site/scss/site.scss` starts with `@use '../../app/scss/tokens'`, so the greys,
the blue, the severity red and amber, the radii and the fixed sizes on this
page are the values the product paints with. There is no second palette to keep
in sync, and changing the app's changes the page.

Two things are the page's own, on purpose:

- **A sans for prose.** The app is monospace throughout, because everything on
  its screen is an identifier. A page of sentences set that way reads as a
  terminal emulator rather than as a product, so prose is Instrument Sans and
  every identifier, command and column stays in the app's Roboto Mono.
- **Dark only.** The app supports both themes. A marketing page having one
  opinion, and that opinion matching the app's default, is worth more here than
  a toggle nobody asked for.

## The explorer is a drawing

In v1 the page embedded the real webapp, fed by a synthetic socket. That cannot
work in v2: the explorer is a query client for a Go server that holds a stream
open, compiles rQL and reads Parquet, and a static deployment is none of those
things. Faking it convincingly would mean reimplementing the query engine in
the page, which is a worse lie than a drawing.

So the explorer on the page is markup — but markup dimensioned by the app's own
tokens: `--line-h`, `--head-h`, `--gutter-w`, `--sev-w`, `--sidebar-w`,
`--chart-h`. The row height and the timestamp gutter are the product's, and the
colours are the product's. The layout can age; the measurements cannot silently
disagree.

The same rule governs the rQL demo further down: every result set it switches
between is in the HTML already. Nothing on that panel is generated, because a
query whose results were invented in JavaScript is exactly the claim this page
should not make.

## Deployment

Vercel project `rtail`, on the personal team. `main` is the production branch,
so a push that touches `site/` deploys it, and pull requests get preview
deployments.

The build is driven by [`vercel.json`](../vercel.json) at the repo root —
`buildCommand: npm run build:site`, `outputDirectory: site/dist`,
`cleanUrls: true` so `/docs` serves `docs.html`. The framework preset is
"Other"; there is nothing to auto-detect. A commit touching only the server
still redeploys the page, which is harmless: the build is a couple of seconds
and the output is identical.

`rtail.ciuffolo.com` is assigned to the production environment. The apex
`ciuffolo.com` is registered on Vercel, so the subdomain needed no DNS record.

**While v2 lives on its own branch**, production still serves whatever `main`
has. Point the Vercel project at this branch, or wait for the merge, before
expecting the new page at that URL.
