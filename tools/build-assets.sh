#!/bin/sh
#
# Build every generated asset the webapp needs. Run inside the tools container.
#
#   app/css/main.css     <- app/scss/main.scss        (dart-sass)
#   app/app.js           <- app/app.ejs               (ejs)
#   app/vendor/highlight.js                           (esbuild)

set -e

echo "  - sass    app/scss/main.scss -> app/css/main.css"
sass --no-source-map app/scss/main.scss app/css/main.css

echo "  - ejs     app/app.ejs -> app/app.js"
node tools/render-app-js.js

echo "  - esbuild highlight.js -> app/vendor/highlight.js"
mkdir -p app/vendor
esbuild tools/hljs-entry.js \
  --bundle \
  --format=iife \
  --minify \
  --log-level=warning \
  --outfile=app/vendor/highlight.js
