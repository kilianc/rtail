/**
 * Main JS controller for webapp
 */

angular
  .module('app', ['$ansiToHtml'])
  .controller('MainCtrl', ['$scope', '$sce', '$ansiToHtml', function MainCtrl($scope, $sce, $ansiToHtml) {
    var format = Format()

    $scope.lines = []
    $scope.socket = io(document.location.origin, { path: document.location.pathname + 'socket.io' })

    $scope.socket.on('streams', function (streams) {
      $scope.streams = streams
      $scope.$apply()
    })

    $scope.selectStream = function (stream) {
      $scope.socket.emit('select stream', stream)
    }

    function formatLine(line){
      // skip objects
      if ('object' === line.type) return JSON.stringify(line, null, '  ')

      line.line = $ansiToHtml.toHtml(line.line)
      line.line = $sce.trustAsHtml(line.line)
      return line
    }

    $scope.socket.on('backlog', function (lines) {
      $scope.lines = lines.map(formatLine)
      $scope.$apply()
    })


    $scope.socket.on('line', function (line) {
      $scope.lines.push(formatLine(line))
      $scope.lines.length >= 100 && $scope.lines.shift()
      $scope.$apply()

      $('body').scrollTop($('body')[0].scrollHeight)
    })
}])