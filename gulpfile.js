var gulp = require('gulp')
var run = require('run-sequence')
var plugins = require('gulp-load-plugins')()
var del = require('del')
var autoprefixer = require('autoprefixer-core')
var express = require('express')

/**
 * Clean builds
 */

gulp.task('clean:dist', function (done) {
  del('dist', done)
})

gulp.task('clean:sass', function (done) {
  del('webapp/css/*.css*', done)
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
 * Copy npm deps
 */

gulp.task('copy:deps', function () {
  return gulp.src([
    'node_modules/angular/angular.js',
    'node_modules/angular-animate/angular-animate.js',
    'node_modules/jquery/dist/jquery.js',
    'node_modules/moment/moment.js',
    'node_modules/angular-moment/angular-moment.js',
    'node_modules/localforage/dist/localforage.js',
    'node_modules/angular-localforage/dist/angular-localForage.js',
    'node_modules/angular-rt-popup/dist/angular-rt-popup.js'
  ])
    .pipe(gulp.dest('webapp/scripts'))
})

/**
 * Optimize images
 */

gulp.task('copy:images', function () {
  return gulp.src(['webapp/images/**/*', '!app/images/**/*.svg'])
    .pipe(plugins.imagemin({ optimizationLevel: 3, progressive: true, interlaced: true }))
    .pipe(gulp.dest('dist/images'))
})

/**
 * Copy SVG
 */

gulp.task('copy:svg', function () {
  return gulp.src('webapp/images/**/*.svg')
    .pipe(gulp.dest('dist/images'))
})

/**
 * Compile CSS
 */

gulp.task('sass', function () {
  return gulp.src('webapp/css/*.scss', { base: 'webapp' })
    .pipe(plugins.sourcemaps.init())
    .pipe(plugins.sass())
    .on('error', function (err) {
      plugins.util.log('sass error', err.message)
      plugins.util.beep()
    })
    .pipe(plugins.postcss([ autoprefixer({ browsers: ['last 2 version'] }) ]))
    .pipe(plugins.sourcemaps.write())
    .pipe(gulp.dest('webapp'))
})

/**
 * Launch server + livereload in dev mode
 */

gulp.task('webapp', ['build:webapp'], function (done) {
  gulp.watch('webapp/css/*.scss', ['sass'])

  plugins.livereload({ start: true })

  gulp.watch([
    'webapp/**/*',
    '!webapp/css/*.scss',
    '!webapp/css/*.css.map',
  ]).on('change', function (file) {
    plugins.livereload.changed(file.path)
  })

  var PORT = process.env.PORT || 3000
  var BASE = process.env.BASE || '/'
  var DIR = '/webapp'

  var server = express()
    .use(BASE, express.static(__dirname + DIR, { etag: false }))

  server.listen(PORT, function () {
    plugins.util.log('Express server listening at http://localhost:' + PORT + BASE)
    done()
  })
})

/**
 * Build app
 */

gulp.task('build:webapp', function (done) {
  run('clean:sass', 'sass', 'copy:deps', done)
})

/**
 * Default task
 */

gulp.task('default', ['build:webapp'])