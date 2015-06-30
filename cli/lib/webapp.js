/*!
 * webapp.js
 * Created by Kilian Ciuffolo on Nov 11, 2014
 * (c) 2014-2015
 */

'use strict'

const debug = require('debug')('rtail:webapp')
const get = require('request').defaults({ encoding: null })

// serve frontend from s3
module.exports = function webapp(opts) {
  let cache = Object.create(null)
  let cacheTTL = opts.cacheTTL
  let s3 = opts.s3

  /*!
   * middleware
   */
  return function (req, res) {
    if (cache[req.path]) {
      return serveCache(req, res)
    }

    debug('caching %s', req.path)

    get(s3 + req.path, function (err, s3res, body) {
      cache[req.path] = {
        headers: s3res.headers,
        body: body
      }

      serveCache(req, res)
    })
  }

  /*!
   * wipes out cache every cachettl ms
   */
  setInterval(function () {
    cache = Object.create(null)
  }, cacheTTL)

  /*!
   * serves req from cache
   */
  function serveCache(req, res) {
    debug('serving from cache %s', req.path)
    res.writeHead(200, cache[req.path].headers)
    res.end(cache[req.path].body)
  }
}
