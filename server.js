/*!
 * server.js
 * Created by Kilian Ciuffolo on Jun 29, 2015
 * (c) 2015
 */

'use strict'

const express = require('express')
const NODE_ENV = process.env.NODE_ENV

var app = express()
app.use(express.static(__dirname + '/dist'))

if ('prod' !== NODE_ENV) {
  app.use(function (req, res, next) {
    if ('/robots.txt' !== req.url) return next()
    res.type('text/plain')
    res.send('User-agent: *\nDisallow: /')
  })
}

app.listen(8080)
