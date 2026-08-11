/**
 * Render app/app.ejs -> app/app.js.
 *
 * This is the one thing the old `gulp ejs` task did that the dev loop still
 * needs. gulp 3.9 cannot run on Node >= 12 at all ("primordials is not
 * defined"), so the dev loop does it directly instead.
 */

'use strict'

var fs = require('fs')
var path = require('path')
var ejs = require('ejs')

var root = path.join(__dirname, '..')
var version = require(path.join(root, 'package.json')).version
var src = path.join(root, 'app', 'app.ejs')
var dest = path.join(root, 'app', 'app.js')

var rendered = ejs.render(fs.readFileSync(src, 'utf8'), { version: version })

fs.writeFileSync(dest, rendered)
console.log('rendered app/app.js (version ' + version + ')')
