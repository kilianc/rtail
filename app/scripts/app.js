$(function () {
  /*!
   * setup copy to clipboard
   */
  ZeroClipboard.config({ swfPath: './swf/ZeroClipboard.swf' })

  $('.pbcopy').each(function (i, el) {
    var $el = $(el)
    var text = $el.parent().text().replace(/(\$ |  +|⌘ \+ C|\n*.*$)/g, '')
    $el.attr('data-clipboard-text', text)

    var zc = new ZeroClipboard(el)
    zc.on('aftercopy', function (e) {
      setTimeout(function () {
        $el.addClass('animated flash')
        $el.one('webkitAnimationEnd mozAnimationEnd MSAnimationEnd oanimationend animationend', function () {
          $el.removeClass('animated flash')
        })
      }, 100)
    })
  })

  /*!
   * setup GA
   */
  if ('rtail.org' === document.location.host) {
    ;(function(i,s,o,g,r,a,m){i['GoogleAnalyticsObject']=r;i[r]=i[r]||function(){
    (i[r].q=i[r].q||[]).push(arguments)},i[r].l=1*new Date();a=s.createElement(o),
    m=s.getElementsByTagName(o)[0];a.async=1;a.src=g;m.parentNode.insertBefore(a,m)
    })(window,document,'script','//www.google-analytics.com/analytics.js','ga');
    ga('create', 'UA-64479821-2', 'auto');
    ga('send', 'pageview');
  }
})
