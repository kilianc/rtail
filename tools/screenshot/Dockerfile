# tools/screenshot/Dockerfile — the browser that takes the README screenshot.
#
#   make screenshots
#
# Separate from tools/Dockerfile on purpose: Chromium and its system libraries
# are half a gigabyte that nothing but this one target needs, and the
# toolchain image is pulled by every `make` there is. Playwright's own image
# carries the browsers, built for the exact Playwright release below; the
# package is installed to match it, with no install scripts, so nothing is
# downloaded when it runs.
FROM mcr.microsoft.com/playwright:v1.49.1-noble

RUN npm install -g --ignore-scripts playwright@1.49.1

ENV NODE_PATH=/usr/lib/node_modules \
    NPM_CONFIG_UPDATE_NOTIFIER=false \
    NPM_CONFIG_FUND=false

WORKDIR /work
