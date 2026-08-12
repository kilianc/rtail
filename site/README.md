# rtail.ciuffolo.com

The landing page. It is deliberately not part of the product build — `npm run
build` builds the webapp, `npm run build:site` builds this.

    make site         # build into site/dist
    make site-serve   # build it and serve it locally

## The preview is the real app

The frame on the page is not a screenshot. `tools/build-site.ts` bundles
`app/src` unmodified and resolves its `lib/connection.ts` to
[`src/connection.ts`](src/connection.ts), which feeds the app a synthetic
stream. Everything else — components, ANSI and JSON formatting, stylesheet — is
the product, so the preview cannot drift from it.

It has to be synthetic: rtail ingests over UDP and holds a socket.io connection
open, and a static deployment does neither. `src/connection.ts` mirrors
`rtail-server`'s socket contract exactly, so the app cannot tell the difference.

Colours come from [`app/scss/_tokens.scss`](../app/scss/_tokens.scss) rather
than a second palette, and the wordmark is the app's own SVG masked with
`currentColor`. Neither can disagree with the tool.

## Deployment

Vercel project `rtail`, on the personal team. The page ships with the tool:
`main` is the production branch, so a push that touches `site/` deploys it, and
pull requests get the usual preview deployments.

The build is driven by [`vercel.json`](../vercel.json) at the repo root —
`buildCommand: npm run build:site`, `outputDirectory: site/dist`. The framework
preset is "Other"; there is nothing to auto-detect. Note that this means a
commit touching only the CLI still redeploys the page, which is harmless: the
build is a few seconds and the output is identical.

`rtail.ciuffolo.com` is assigned to the production environment. The apex
`ciuffolo.com` is registered on Vercel, so the subdomain needed no DNS record —
before this project existed it resolved to Vercel and served
`DEPLOYMENT_NOT_FOUND`.
