var gulp = require('gulp')
var run = require('run-sequence')
var plugins = require('gulp-load-plugins')()
var del = require('del')
var autoprefixer = require('autoprefixer-core')
var express = require('express')
var version = require('./package.json').version
var spawn = require('child_process').spawn
var get = require('request').defaults({ json: true }).get

/**
 * rTail package URL
 */

var REPO_URL = 'https://registry.npmjs.org/rtail/latest'

/**
 * Clean builds
 */

gulp.task('clean:dist', function (done) {
  del('dist', done)
})

gulp.task('clean:sass', function (done) {
  del('app/css/*', done)
})

gulp.task('clean', function (done) {
  run(['clean:dist', 'clean:sass'], done)
})

/**
 * Clean deps
 */

gulp.task('clean:npm', function (done) {
  del('node_modules', done)
})

/**
 * Compile CSS
 */

gulp.task('sass', function () {
  return gulp.src('app/scss/*', { base: 'app/scss' })
    .pipe(plugins.sourcemaps.init())
    .pipe(plugins.sass())
    .on('error', function (err) {
      plugins.util.beep()
      plugins.util.log('sass error', err.message)
    })
    .pipe(plugins.postcss([ autoprefixer({ browsers: ['last 2 version'] }) ]))
    .pipe(plugins.sourcemaps.write())
    .pipe(gulp.dest('app/css'))
})

/**
 * Compile templates
 */

gulp.task('app.ejs', function () {
  return gulp.src('app/app.ejs')
    .pipe(plugins.ejs({}, { ext: '.js' }))
      .on('error', function (err) {
        plugins.util.log('ejs error', err.message)
        plugins.util.beep()
      })
    .pipe(gulp.dest('app'))
})

gulp.task('index.ejs', function (done) {
  get(REPO_URL, function (err, res, body){
    var data = { tagline: body.description }

    gulp.src('app/index.ejs')
      .pipe(plugins.ejs(data))
        .on('error', function (err) {
          plugins.util.log('ejs error', err.message)
          plugins.util.beep()
        })
      .pipe(gulp.dest('app'))
        .on('end', done)
  })
})

/**
 * Launch server + livereload in dev mode
 */

gulp.task('app', ['build:app'], function (done) {
  gulp.watch('app/scss/*', ['sass'])
  gulp.watch('app/app.ejs', ['app.ejs'])
  gulp.watch('app/index.ejs', ['index.ejs'])

  plugins.livereload({ start: true })

  gulp.watch([
    'app/**/*',
    '!app/app.ejs',
    '!app/index.ejs',
    '!app/scss/*',
  ]).on('change', function (file) {
    plugins.livereload.changed(file.path)
  })

  var BASE = '/app'
  var PORT = process.env.PORT || 3000
  var server = express()

  ;[
    BASE,
    '/bower_components',
    '/node_modules'
  ].forEach(function (folder) {
    server.use(folder, express.static(__dirname + folder, { etag: false }))
  })

  server.listen(PORT, function () {
    plugins.util.log('Express server listening at http://localhost:' + PORT + BASE)
    done()
  })
})

/**
 * Build app
 */

gulp.task('build:app', function (done) {
  run('clean:sass', ['sass', 'app.ejs', 'index.ejs', 'copy:zc:app'], done)
})

/**
 * Copy images
 */

gulp.task('copy:images', function () {
  return gulp.src('app/images/**/*')
    .pipe(gulp.dest('dist/images'))
})

/**
 * Copy zeroclipboard
 */

gulp.task('copy:zc:app', function () {
  var base = 'node_modules/zeroclipboard/dist/'
  return gulp.src(base + '/ZeroClipboard.swf', { base: base })
    .pipe(gulp.dest('app/swf'))
})

gulp.task('copy:zc:dist', function () {
  var base = 'node_modules/zeroclipboard/dist/'
  return gulp.src(base + '/ZeroClipboard.swf', { base: base })
    .pipe(gulp.dest('dist/swf'))
})

/**
 * Bundle CSS / JS
 */

gulp.task('html', function () {
  var assets = plugins.useref.assets()

  return gulp.src('app/index.html')
    .pipe(assets)
    .pipe(assets.restore())
    .pipe(plugins.useref())
    .pipe(gulp.dest('dist'))
})

/**
 * Minify JS
 */

gulp.task('minify:js', function () {
  return gulp.src('dist/bundle.min.js')
    .pipe(plugins.uglify())
    .pipe(gulp.dest('dist/'))
})

/**
 * Minify CSS
 */

gulp.task('minify:css', function () {
  return gulp.src('dist/css/bundle.min.css')
    .pipe(plugins.minifyCss( { keepSpecialComments: 0, keepBreaks: true }))
    .pipe(gulp.dest('dist/css/'))
})

/**
 * Minify HTML
 */

gulp.task('minify:html', function () {
  return gulp.src('dist/index.html')
    .pipe(plugins.minifyHtml( { conditionals: true, quotes: true }))
    .pipe(gulp.dest('dist/'))
})

/**
 * Minify all
 */

gulp.task('minify', function (done) {
  run(['minify:js', 'minify:css', 'minify:html'], done)
})

/**
 * Build dist
 */

gulp.task('build:dist', function (done) {
  run(['build:app', 'clean:dist'], ['copy:images', 'copy:zc:dist'], 'html', 'minify', done)
})

/**
 * Default task
 */

gulp.task('default', ['build:dist'])
