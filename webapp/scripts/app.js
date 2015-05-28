/**
 * Main JS controller for webapp
 */

angular
  .module('app', [
    'ngAnimate',
    '$ansiToHtml',
    'angularMoment',
    'LocalForageModule',
    'rt.popup'
  ])
  .controller('MainController', function MainController($scope, $injector) {
    var $sce = $injector.get('$sce')
    var $ansiToHtml = $injector.get('$ansiToHtml')
    var $moment = $injector.get('moment')
    var $localForage = $injector.get('$localForage')
    var $streamLines = $('.stream-lines')
    var ctrl = this
    var BUFFER_SIZE = 100

    ctrl.paused = false
    ctrl.version = '0.2.0'
    ctrl.lines = []

    /**
     * Socket stuff
     */

    ctrl.socket = io(document.location.origin, { path: document.location.pathname + 'socket.io' })

    ctrl.socket.on('streams', function (streams) {
      ctrl.streams = streams
      $scope.$apply()
    })

    ctrl.socket.on('backlog', function (lines) {
      if (!lines) return
      ctrl.lines = lines.map(formatLine)
      $scope.$apply()
      updateScroll()
      console.info('%s: backlog received %d', ctrl.activeStream, lines.length)
    })

    ctrl.socket.on('line', function (line) {
      ctrl.lines.push(formatLine(line))

      if (ctrl.lines.length > BUFFER_SIZE) {
        ctrl.lines.length >= 100 && ctrl.lines.shift()
      }

      $scope.$apply()
      updateScroll()
    })

    ctrl.selectStream = function selectStream(stream) {
      ctrl.activeStream = stream
      ctrl.socket.emit('select stream', stream)
      ctrl.resume()
      $localForage.setItem('activeStream', stream)
    }

    ctrl.isSelected = function isSelected(stream) {
      return ctrl.activeStream === stream
    }

    function updateScroll() {
      var where = ctrl.streamDirection ? $streamLines[0].scrollHeight : 0
      $streamLines.scrollTop(where)
    }

    function formatLine(line) {
      // for object just format JSON
      if ('object' === line.type) {
        line.html = $sce.trustAsHtml('<pre>' + JSON.stringify(line.content, null, '  ') + '</pre>')
      } else {
        // for log lines format ansi format
        line.html = $ansiToHtml.toHtml(line.content || line.line)
        line.html = $sce.trustAsHtml(line.html)
      }

      return line
    }

    ctrl.resume = function resume() {
      if (!ctrl.paused) return
      ctrl.paused = false
      ctrl.socket.emit('select stream', ctrl.activeStream)
      $streamLines.on('mousewheel', ctrl.pause)
    }

    ctrl.pause = function pause() {
      if (ctrl.paused) return
      ctrl.paused = true
      ctrl.socket.emit('select stream')
      $streamLines.off('mousewheel', ctrl.pause)
      $scope.$apply()
    }

    $streamLines.on('mousewheel', ctrl.pause)

    /**
     * Settings and preferences
     */

    ctrl.toggleFavorite = function toggleFavorite(stream) {
      if (ctrl.favorites[stream]) {
        delete ctrl.favorites[stream]
      } else {
        ctrl.favorites[stream] = true
      }

      $localForage.setItem('favorites', ctrl.favorites)
    }

    ctrl.setTheme = function setTheme(theme) {
      ctrl.theme = theme
      $localForage.setItem('theme', theme)
    }

    ctrl.setFontFamily = function setFontFamily(fontFamily) {
      ctrl.fontFamily = fontFamily
      $localForage.setItem('fontFamily', fontFamily)
    }

    ctrl.incFontSize = function incFontSize(fontSize) {
      ctrl.fontSize = Math.min(7, ctrl.fontSize + 1)
      $localForage.setItem('fontSize', ctrl.fontSize)
    }

    ctrl.resetFontSize = function resetFontSize() {
      ctrl.fontSize = 4
      $localForage.setItem('fontSize', ctrl.fontSize)
    }

    ctrl.decFontSize = function decFontSize(fontSize) {
      ctrl.fontSize = Math.max(1, ctrl.fontSize - 1)
      $localForage.setItem('fontSize', fontSize)
    }

    ctrl.setStreamDirection = function setStreamDirection(streamDirection) {
      ctrl.streamDirection = streamDirection
      $localForage.setItem('streamDirection', streamDirection)
    }

    /**
     * Load storage in memory and boot
     */

    $localForage.getItem('theme').then(function (theme) {
      ctrl.theme = theme || 'dark'
    })

    $localForage.getItem('fontFamily').then(function (fontFamily) {
      ctrl.fontFamily = fontFamily || 1
    })

    $localForage.getItem('fontSize').then(function (fontSize) {
      ctrl.fontSize = fontSize || 4
    })

    $localForage.getItem('favorites').then(function (favorites) {
      ctrl.favorites = favorites || {}
    })

    $localForage.getItem('activeStream').then(function (activeStream) {
      if (!activeStream) return
      console.info('%s: restoring session', activeStream)
      ctrl.selectStream(activeStream)
    })

    $localForage.getItem('streamDirection').then(function (streamDirection) {
      ctrl.streamDirection = undefined === streamDirection ? true : streamDirection
    })
  })