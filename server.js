/*!
 * server.js
 * Created by Kilian Ciuffolo on Jun 29, 2015
 * (c) 2015
 */

'use strict'

const express = require('express')

var app = express()
app.use(express.static(__dirname + '/dist'))
app.listen(8080)
