var debug = require('debug')('rtail:proxy')
  , get = require('request').defaults({ encoding: null })

var s3, cacheTTL
var cache = Object.create(null)

// serve frontend from s3
module.exports = function website(opts) {
  cacheTTL = opts.cacheTTL
  s3 = opts.s3

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
        body: cache[req.path] = body
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