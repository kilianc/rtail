/*!
 * server.js
 * Created by Kilian Ciuffolo on Oct 26, 2014
 * (c) 2014-2015
 */

'use strict'

const express = require('express')

var app = express()
app.use(express.static(__dirname + '/dist'))
app.listen(8888)
