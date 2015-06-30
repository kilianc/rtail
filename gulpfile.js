var gulp = require('gulp')
var run = require('run-sequence')
var plugins = require('gulp-load-plugins')()
var del = require('del')
var autoprefixer = require('autoprefixer-core')
var express = require('express')
var version = require('./package.json').version
var spawn = require('child_process').spawn

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
      plugins.util.log('sass error', err.message)
      plugins.util.beep()
    })
    .pipe(plugins.postcss([ autoprefixer({ browsers: ['last 2 version'] }) ]))
    .pipe(plugins.sourcemaps.write())
    .pipe(gulp.dest('app/css'))
})

/**
 * Compile templates
 */

gulp.task('ejs', function () {
  return gulp.src('app/app.ejs')
    .pipe(plugins.ejs({ version: version }, { ext: '.js' }))
      .on('error', function (err) {
        plugins.util.log('ejs error', err.message)
        plugins.util.beep()
      })
    .pipe(gulp.dest('app'))
})


/**
 * Compile templates
 */

gulp.task('hjs', function (done) {
  var opts = {
    cwd: __dirname + '/node_modules/highlight.js'
  }

  var npmInstall = spawn('npm', ['install'], opts)
  npmInstall.stdout.pipe(process.stdout)
  npmInstall.stderr.pipe(process.stderr)

  npmInstall.on('close', function (code) {
    if (0 !== code) throw new Error('npm install exited with ' + code)

    var build = spawn('node', ['tools/build.js', '-n', 'json'], opts)
    build.stdout.pipe(process.stdout)
    build.stderr.pipe(process.stderr)

    build.on('close', function (code) {
      if (0 !== code) throw new Error('node tools/build.js exited with ' + code)
      done()
    })
  })
})

/**
 * Launch server + livereload in dev mode
 */

gulp.task('app', ['build:app'], function (done) {
  gulp.watch('app/scss/*.scss', ['sass'])
  gulp.watch('app/app.ejs', ['ejs'])

  plugins.livereload({ start: true })

  gulp.watch([
    'app/**/*',
    '!app/app.ejs',
    '!app/scss/*',
  ]).on('change', function (file) {
    plugins.livereload.changed(file.path)
  })

  plugins.util.log('spinning rtail client and server ... http://localhost:8888/app')

  var rTailServer = spawn('node', ['--harmony', 'cli/rtail-server.js', '--web-version', 'development'])
  rTailServer.stdout.pipe(process.stdout)
  rTailServer.stderr.pipe(process.stdout)

  var rTailClient = spawn('node', ['--harmony', 'cli/rtail.js'])
  rTailClient.stdout.pipe(process.stdout)
  rTailClient.stderr.pipe(process.stdout)

  var lines = [
    '200 GET /1/geocode?address=ny',
    '200 GET /1/config',
    '500 GET /1/users/556605ede9fa35333befa9e6/profile',
    '200 POST /1/signin',
    '200 GET /1/users/556605ede9fa35333befa9e6/profile',
    '200 PUT /1/me/gcm_tokens/duUOo8jRIxq547jAaAHvsF9v',
    '200 PUT /1/me/review_status/seen',
    '301 GET /1/config',
    '200 GET /1/users/555f7494e9fa35333befa9ab/profile',
    '200 POST /1/signin',
    '200 GET /1/users/555f7494e9fa35333befa9ab/profile',
    '400 PUT /1/me/gcm_tokens/3G7ggYFcGXIHkIgaGLW16s4sobrkAPA91bGM8t9MJwfDbFA',
    '200 GET /1/me/notifications',
    '200 GET /1/me/picture',
    '200 GET /1/alive'
  ]

  function log2rtail(str) {
    rTailClient.stdin.write(str + '\n')
  }

  setInterval(function () {
    var debug = require('debug')('api:logs')
    var index = Math.round(Math.random() * lines.length)
    var line = lines[index]

    debug.log = log2rtail

    if (Math.random() < 0.8) {
      debug(line)
    } else {
      log2rtail(JSON.stringify({
        foo: 'bar',
        bar: 'foo',
        count: Math.random() * 1000,
        list: [
          "foo",
          "bar"
        ],
        doc: {
          foo: 'bar',
          bar: 'foo'
        },
        link: "http://google.com",
        regexp: /a.?/,
        color: "#fff"
      }))
    }
  }, 1000)
})

/**
 * Build app
 */

gulp.task('build:app', function (done) {
  run('clean:sass', 'sass', 'ejs', 'hjs', done)
})

/**
 * Copy SVG
 */

gulp.task('copy:images', function () {
  return gulp.src('app/images/**/*')
    .pipe(gulp.dest('dist/images'))
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
    .pipe(plugins.ngAnnotate())
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
  run(['build:app', 'clean:dist'], 'copy:images', 'html', 'minify', done)
})

/**
 * Default task
 */

gulp.task('default', ['build:dist'])