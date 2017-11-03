/* global hljs */
/* eslint-disable import/no-extraneous-dependencies */

require('./scss/main.scss');
require('./scss/dark-theme.scss');
require('./scss/light-theme.scss');

const $ = require('jquery');
const io = require('socket.io-client');
const ansiUp = require('ansi_up');
const angular = require('angular');
const ngAnimate = require('angular-animate');
const localForage = require('angular-localforage');
require('angular-moment');
require('angular-ui-router');
// require('angular-rt-popup');

const BUFFER_SIZE = 100;

/*!
 * main js controller for webapp
 */
angular
  .module('app', [
    ngAnimate,
    localForage,
    // 'rt.popup',
    'angularMoment',
    'ui.router',
  ])
  .config(($stateProvider) => {
    $stateProvider.state('streams', {
      url: '/streams/:stream',
    });
  })
  .directive('resizable', $localForage => ({
    restrict: 'A',
    link($scope, $element) {
      const $window = $(window);
      const $body = $('body');
      const handler = $element.find('.resize-handler');
      const minWidth = $element.width();
      const onMouseMove = (e) => {
        const width = Math.min(600, Math.max(minWidth, e.clientX));
        $element.css('width', width);
      };

      handler.on('mousedown', () => {
        $body.addClass('resizing');
        $window.on('mousemove', onMouseMove);
      });

      $window.on('mouseup', () => {
        $body.removeClass('resizing');
        $window.off('mousemove', onMouseMove);
        $localForage.setItem('sidebarWidth', $element.width());
      });
    },
  }))
  .controller('MainController', function MainController($scope, $injector) {
    const $sce = $injector.get('$sce');
    const $localForage = $injector.get('$localForage');
    const $rootScope = $injector.get('$rootScope');
    const $state = $injector.get('$state');
    const $stateParams = $injector.get('$stateParams');
    const $streamLines = $('.stream-lines');
    const ctrl = this;

    ctrl.paused = false;
    ctrl.version = '0.2.1';
    ctrl.lines = [];

    /*!
     * socket stuff
     */
    ctrl.socket = io(document.location.origin, { path: `${document.location.pathname}socket.io` });

    // https://github.com/component/escape-html/blob/master/index.js#L22
    const escapeHtml = html => String(html)
      .replace(/&/g, '&amp;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;');

    const updateScroll = () => {
      const where = ctrl.streamDirection ? $streamLines[0].scrollHeight : 0;
      $streamLines.scrollTop(where);
    };

    const formatLine = (line) => {
      if (!line.content) {
        // handle empty line
        line.html = $sce.trustAsHtml('');
      } else if (line.type === 'object') {
        // for object just format JSON
        line.html = hljs.highlight('json', JSON.stringify(line.content, null, '  ')).value;
        line.html = $sce.trustAsHtml(`<pre>${line.html}</pre>`);
      } else {
        // for log lines use ansi format
        line.html = escapeHtml(line.content);
        line.html = ansiUp.ansi_to_html(line.html, { use_classes: true });
        line.html = $sce.trustAsHtml(line.html);
      }

      return line;
    };

    const formatStreams = streamList => streamList.reduce((result, streamName) => {
      const splitRegex = /(.+)!\*!(.+)/;
      const match = streamName.match(splitRegex);
      if (!match) {
        result[streamName] = {
          name: streamName,
          title: streamName,
        };
        return result;
      }

      const category = match[1];
      const title = match[2];
      if (!result[category]) {
        result[category] = {
          title: category,
          childs: [],
        };
      }

      result[category].childs.push({
        name: streamName,
        title,
      });

      return result;
    }, {});

    ctrl.socket.on('streams', (streams) => {
      ctrl.streams = streams;
      ctrl.streamHash = formatStreams(streams);
      $scope.$apply();
    });

    ctrl.socket.on('backlog', (lines) => {
      if (!lines) return;
      ctrl.lines = lines.map(formatLine);
      $scope.$apply();
      updateScroll();
    });

    ctrl.socket.on('line', (line) => {
      if (ctrl.lines.length > BUFFER_SIZE) {
        ctrl.lines.shift();
      }

      ctrl.lines.push(formatLine(line));
      $scope.$apply();
      updateScroll();
    });

    ctrl.selectStream = function selectStream(stream) {
      ctrl.lines = [];
      if (ctrl.activeStream) {
        ctrl.socket.emit('streamUnsubscribe', ctrl.activeStream);
      }

      ctrl.activeStream = stream;
      ctrl.socket.emit('streamSubscribe', stream);
      ctrl.resume();
      $localForage.setItem('activeStream', stream);
    };

    ctrl.isSelected = function isSelected(stream) {
      return ctrl.activeStream === stream;
    };

    ctrl.activeStreamFilter = function activeStreamFilter(line) {
      try {
        return (new RegExp(ctrl.activeStreamRegExp)).test(line.content);
      } catch (err) {
        return true;
      }
    };

    ctrl.resume = function resume() {
      if (!ctrl.paused) return;
      ctrl.paused = false;
      ctrl.socket.emit('select stream', ctrl.activeStream);
      $streamLines.on('mousewheel', ctrl.pause);
    };

    ctrl.pause = function pause() {
      if (ctrl.paused) return;
      ctrl.paused = true;
      ctrl.socket.emit('select stream');
      $streamLines.off('mousewheel', ctrl.pause);
      $scope.$apply();
    };

    $streamLines.on('mousewheel', ctrl.pause);

    /*!
     * settings and preferences
     */
    ctrl.toggleFavorite = function toggleFavorite(stream) {
      if (ctrl.favorites[stream]) {
        delete ctrl.favorites[stream];
      } else {
        ctrl.favorites[stream] = true;
      }

      $localForage.setItem('favorites', ctrl.favorites);
    };

    ctrl.toggleTimestamp = function toggleTimestamp(stream) {
      if (ctrl.hiddenTimestamps[stream]) {
        delete ctrl.hiddenTimestamps[stream];
      } else {
        ctrl.hiddenTimestamps[stream] = true;
      }

      $localForage.setItem('hiddenTimestamps', ctrl.hiddenTimestamps);
    };

    ctrl.setTheme = function setTheme(theme) {
      ctrl.theme = theme;
      $localForage.setItem('theme', theme);
    };

    ctrl.setFontFamily = function setFontFamily(fontFamily) {
      ctrl.fontFamily = fontFamily;
      $localForage.setItem('fontFamily', fontFamily);
    };

    ctrl.incFontSize = function incFontSize() {
      ctrl.fontSize = Math.min(7, ctrl.fontSize + 1);
      $localForage.setItem('fontSize', ctrl.fontSize);
    };

    ctrl.resetFontSize = function resetFontSize() {
      ctrl.fontSize = 4;
      $localForage.setItem('fontSize', ctrl.fontSize);
    };

    ctrl.decFontSize = function decFontSize(fontSize) {
      ctrl.fontSize = Math.max(1, ctrl.fontSize - 1);
      $localForage.setItem('fontSize', fontSize);
    };

    ctrl.setStreamDirection = function setStreamDirection(streamDirection) {
      ctrl.streamDirection = streamDirection;
      $localForage.setItem('streamDirection', streamDirection);
    };

    /*!
     * load storage in memory and boot
     */
    $localForage.getItem('sidebarWidth').then((sidebarWidth) => {
      ctrl.sidebarWidth = sidebarWidth;
    });

    $localForage.getItem('theme').then((theme) => {
      ctrl.theme = theme || 'dark';
    });

    $localForage.getItem('fontFamily').then((fontFamily) => {
      ctrl.fontFamily = fontFamily || 1;
    });

    $localForage.getItem('fontSize').then((fontSize) => {
      ctrl.fontSize = fontSize || 4;
    });

    $localForage.getItem('favorites').then((favorites) => {
      ctrl.favorites = favorites || {};
    });

    $localForage.getItem('hiddenTimestamps').then((hiddenTimestamps) => {
      ctrl.hiddenTimestamps = hiddenTimestamps || {};
    });

    $localForage.getItem('activeStream').then((activeStream) => {
      if (!activeStream || ($state.current.name === 'streams' && $stateParams.stream)) return;
      $state.go('streams', { stream: activeStream });
    });

    $localForage.getItem('streamDirection').then((streamDirection) => {
      ctrl.streamDirection = undefined === streamDirection ? true : streamDirection;
    });

    /*!
     * respond to url change
     */
    $rootScope.$on('$stateChangeStart', (e, toState, toParams) => {
      if (toState.name !== 'streams') return;
      ctrl.selectStream(toParams.stream);
    });

    /*!
     * tell UI we're ready to roll
     */
    ctrl.loaded = true;
  });
