/**
 * Main JS controller for webapp
 */

angular
  .module('app', [])
  .controller('MainCtrl', ['$scope', '$sce', function MainCtrl($scope, $sce) {

    var format = Format();

    $scope.lines = []
    $scope.socket = io()




    $scope.socket.on('streams', function (streams) {
      $scope.streams = streams
      console.log($scope.streams)
      $scope.$apply()
    })


    $scope.selectStream = function (stream) {
      $scope.socket.emit('select stream', stream)
    }


    function formatLine(line){
      line.line = format.line(line.line)
      line.line = $sce.trustAsHtml(line.line)
      return line;
    }


    $scope.socket.on('backlog', function (lines) {

      for(var i=0; i<lines.length; i++){
        lines[i] = formatLine(lines[i])
      }

      $scope.lines = lines
      $scope.$apply()
    })


    $scope.socket.on('line', function (line) {
      line = formatLine(line)
      $scope.lines.push(line)
      $scope.lines.length >= 100 && $scope.lines.shift()
      $scope.$apply()
      $('body').scrollTop($('body')[0].scrollHeight)
    })
}])


