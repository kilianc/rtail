$(function () {
  var TYPE_SPEED = 150
  var DETELE_SPEED = 20
  var PAUSE_TIME = 3000
  var $command = $('.type-fx span.command')

  var i = 0
  var commands = $command.toArray().map(function (command) {
    return $(command).text()
  })

  $command = $command.first()
  $command.nextAll('.command').remove()

  function clearCommand() {
    var interval = setInterval(function () {
      var oldCommand = $command.text()

      if (0 === oldCommand.length) {
        clearInterval(interval)
        setTimeout(setCommand, 0) // avoid long stacks
        return
      }

      $command.text(oldCommand.slice(0, -1))
    }, DETELE_SPEED)
  }

  function setCommand() {
    i = (i + 1) % commands.length
    var newCommand = commands[i]
    var charIndex = 0

    var interval = setInterval(function () {
      if (charIndex > newCommand.length) {
        clearInterval(interval)
        setTimeout(clearCommand, PAUSE_TIME)
        return
      }
      $command.text(newCommand.slice(0, charIndex ++))
    }, TYPE_SPEED - (100 * Math.random() + 20))
  }

  if (!window.location.search.match(/print/gi)) {
    setTimeout(clearCommand, PAUSE_TIME)
  }
})
