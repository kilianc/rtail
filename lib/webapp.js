var debug = require('debug')('rtail:webapp')
  , get = require('request').defaults({ encoding: null })

// serve frontend from s3
module.exports = function webapp(opts) {
  var cache = Object.create(null)
  var cacheTTL = opts.cacheTTL
  var s3 = opts.s3

  /**
   * Middleware
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

  /**
   * Wipes out cache every cacheTTL ms
   */

  setInterval(function () {
    cache = Object.create(null)
  }, cacheTTL)

  /**
   * Serves req from cache
   */

  function serveCache(req, res) {
    debug('serving from cache %s', req.path)
    res.writeHead(200, cache[req.path].headers)
    res.end(cache[req.path].body)
  }
}