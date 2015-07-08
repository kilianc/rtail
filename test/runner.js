/*!
 * runner.js
 * Created by Kilian Ciuffolo on Jul 7, 2015
 * (c) 2015
 */

'use strict'

const path = require('path')
const Mocha = require('mocha')

const argv = process.argv
const NODE_ENV = process.env.NODE_ENV
const regExp = new RegExp((argv[2] || '').trim() || '.')

console.log('  Running test suite with NODE_ENV=%s (%s)', NODE_ENV, regExp)

let mocha = new Mocha()
mocha.suite.bail(true)
mocha.reporter('spec')
mocha.useColors(true)

;[
  'rtail-client',
  'rtail-server'
].forEach(function (file) {
  if (!regExp.test(file)) return
  mocha.addFile(path.join(__dirname, '../', 'test', file + '.test.js'))
})

mocha.run(process.exit)
