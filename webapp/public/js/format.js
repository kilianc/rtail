/**
 * Formatting module
 *
 * Formats ANSI lines to HTML format and converts JSON data
 * to interactive elements.
 *
 * Credits:
 *
 * ansispan - @mmalecki <https://github.com/mmalecki/ansispan>
 *
 */

var Format = function(){

  // private variables

  var theme = {
    foregroundColors: {
      '30': '#000000',
      '31': '#b04b57',
      '32': '#87b379',
      '33': '#e5c179',
      '34': '#7d8fa4',
      '35': '#a47996',
      '36': '#85a7a5',
      '37': '#ffffff'
    }
  }

  // public

  return {


    /**
     * ANSI line formatting, based on 'ansispan' by @mmalecki
     * <https://github.com/mmalecki/ansispan>
     *
     * @param  {[String]} str [ANSI string]
     * @return {[String]}     [formatted HTML string]
     */
    line: function (str) {
      Object.keys(theme.foregroundColors).forEach(function (ansi) {
        var span = '<span style="color: ' + theme.foregroundColors[ansi] + '">'

        str = str.replace(
          new RegExp('\033\\[' + ansi + 'm', 'g'),
          span
        ).replace(
          new RegExp('\033\\[0;' + ansi + 'm', 'g'),
          span
        )
      })

      str = str.replace(/\033\[1m/g, '<b>').replace(/\033\[22m/g, '</b>')
      str = str.replace(/\033\[3m/g, '<i>').replace(/\033\[23m/g, '</i>')
      str = str.replace(/\033\[m/g, '</span>')
      str = str.replace(/\033\[0m/g, '</span>')

      return str.replace(/\033\[39m/g, '</span>')
    }
  }
}


