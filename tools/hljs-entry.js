// Browser bundle entry for highlight.js.
//
// highlight.js stopped shipping a prebuilt browser bundle in v11, and the app
// loads its vendor scripts with plain <script> tags. Bundling just the core
// plus the one grammar the webapp uses (JSON, for object log lines) keeps the
// payload to a fraction of the old "every language" pack.

const hljs = require('highlight.js/lib/core')

hljs.registerLanguage('json', require('highlight.js/lib/languages/json'))

globalThis.hljs = hljs
