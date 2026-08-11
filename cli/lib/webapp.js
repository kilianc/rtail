/*!
 * webapp.js
 * Created by Kilian Ciuffolo on Nov 11, 2014
 *
 * Serves the published webapp build, caching each asset in memory for `ttl`.
 */

import createDebug from 'debug'

const debug = createDebug('rtail:webapp')

const FORWARDED_HEADERS = ['content-type', 'content-encoding', 'etag', 'last-modified']

/**
 * @param {{ origin: string, ttl: number }} opts
 * @returns {import('express').RequestHandler}
 */
export function webapp(opts) {
  let cache = new Map()

  /*!
   * wipes out cache every ttl ms
   */
  const timer = setInterval(() => {
    cache = new Map()
    debug('cleared cache')
  }, opts.ttl)

  // Don't hold the process open just for the cache sweep.
  timer.unref()

  return async function serveWebapp(req, res, next) {
    const hit = cache.get(req.path)

    if (hit) {
      debug('serving from cache %s', req.path)
      res.writeHead(200, hit.headers)
      return res.end(hit.body)
    }

    debug('caching %s', req.path)

    try {
      const upstream = await fetch(opts.origin + req.path)

      if (!upstream.ok) {
        debug('upstream %s for %s', upstream.status, req.path)
        return res.sendStatus(upstream.status)
      }

      const headers = {}
      for (const name of FORWARDED_HEADERS) {
        const value = upstream.headers.get(name)
        if (value) headers[name] = value
      }

      const body = Buffer.from(await upstream.arrayBuffer())
      cache.set(req.path, { headers, body })

      res.writeHead(200, headers)
      res.end(body)
    } catch (err) {
      // A failed upstream fetch used to throw inside the callback and take the
      // whole server down; surface it to express instead.
      debug('upstream error for %s: %s', req.path, err.message)
      next(err)
    }
  }
}
