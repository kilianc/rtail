# `rtail(1)`

[![CI](https://github.com/kilianc/rtail/actions/workflows/ci.yml/badge.svg)](https://github.com/kilianc/rtail/actions/workflows/ci.yml)
[![NPM version](https://img.shields.io/npm/v/rtail.svg?style=flat-square)](https://www.npmjs.com/package/rtail)
[![NPM downloads](https://img.shields.io/npm/dm/rtail.svg?style=flat-square)](https://www.npmjs.com/package/rtail)
[![GitHub Stars](https://img.shields.io/github/stars/kilianc/rtail.svg?style=flat-square)](https://github.com/kilianc/rtail)
[![License](https://img.shields.io/npm/l/rtail.svg?style=flat-square)](https://www.npmjs.com/package/rtail)
[![Gitter](https://img.shields.io/badge/≡_gitter-join_chat_➝-04cd7e.svg?style=flat-square)](https://gitter.im/kilianc/rtail?utm_source=badge&utm_medium=badge&utm_campaign=pr-badge&utm_content=badge)

## Terminal output to the browser in seconds, using UNIX pipes.

`rtail` is a command line utility that grabs every line in `stdin` and broadcasts it over **UDP**. That's it. Nothing fancy. Nothing complicated. Tail log files, app output, or whatever you wish, using `rtail` broadcasting to an `rtail-server` – See multiple streams in the browser, in realtime.

## Running the server

The server ships as a container image — this is the recommended way to run it.

    $ docker run -d --name rtail -p 8888:8888 -p 9999:9999/udp ghcr.io/kilianc/rtail

Open <http://localhost:8888> and start piping. There is a
[`docker-compose.yml`](docker-compose.yml) if you prefer:

    $ docker compose up -d

Options are flags, or `RTAIL_*` environment variables:

    $ docker run -d -p 8080:8080 -p 9999:9999/udp \
        -e RTAIL_WEB_PORT=8080 ghcr.io/kilianc/rtail

    $ docker run -d -p 8888:8888 -p 9999:9999/udp \
        ghcr.io/kilianc/rtail --backlog 500

The image binds `0.0.0.0` inside the container, so **the published ports are
reachable from your whole network**. It has no authentication — keep it on a
trusted network, or put a reverse proxy in front of it.

## Installing the client

The client is a UNIX pipe and belongs on the host whose output you are tailing,
not in the container. It needs Node.js 20 or newer:

    $ npm install -g rtail

`npm install -g rtail` also gives you `rtail-server`, if you would rather run
the server without Docker.

## Web app

![](https://s3.amazonaws.com/rtail/github/dark.png)

![](https://s3.amazonaws.com/rtail/github/light.png)

## Rationale

Whether you deploy your code on remote servers using multiple environments or simply have multiple projects, **you must `ssh` to each machine running your code, in order to monitor the logs in realtime**.

There are many log aggregation tools out there, but few of them are realtime. **Most other tools require you to change your application source code to support their logging protocol/transport**.

`rtail` is meant to be a replacement of [logio](https://github.com/NarrativeScience/Log.io/commits/master), which isn't actively maintained anymore, doesn't support node v0.12., and uses *TCP. (TCP requires strict client / server handshaking, is resource-hungry, and very difficult to scale.)*

**The `rtail` approach is very simple:**
* pipe something into `rtail` using [UNIX I/O redirection](http://www.westwind.com/reference/os-x/commandline/pipes.html) [[2]](http://www.codecoffee.com/tipsforlinux/articles2/042.html)
* broadcast every line using UDP
* `rtail-server`, **if listening**, will dispatch the stream into your browser, using [socket.io](http://socket.io/).

`rtail` is a realtime debugging and monitoring tool, which can display multiple aggregate streams via a modern web interface. **There is no persistent layer, nor does the tool store any data**. If you need a persistent layer, use something like [loggly](https://www.loggly.com/).

## Examples

In your app init script:

    $ node server.js 2>&1 | rtail --id "api.myproject.com"

    $ mycommand | rtail > server.log

    $ node server.js 2>&1 | rtail --mute

Supports JSON5 lines:

    $ while true; do echo [1, 2, 3, "hello"]; sleep 1; done | rtail
    $ echo { "foo": "bar" } | rtail
    $ echo { format: 'JSON5' } | rtail

Using log files (log rotate safe!):

    $ node server.js 2>&1 > log.txt
    $ tail -F log.txt | rtail

For fun and debugging:

    $ cat ~/myfile.txt | rtail
    $ echo "Server rebooted!" | rtail --id `hostname`

## Params

    $ rtail --help
    Usage: cmd | rtail [OPTIONS]

    Options:
      --host, -h     The server host                 [string] [default: "127.0.0.1"]
      --port, -p     The server port                        [string] [default: 9999]
      --id, --name   The log stream id                 [string] [default: (moniker)]
      --mute, -m     Don't pipe stdin with stdout                          [boolean]
      --tty          Keeps ansi colors                     [boolean] [default: true]
      --parse-date   Looks for dates to use as timestamp   [boolean] [default: true]
      --help         Show help                                             [boolean]
      --version, -v  Show version number                                   [boolean]

    Examples:
      server | rtail > server.log         localhost + file
      server | rtail --id api.domain.com  Name the log stream
      server | rtail --host example.com   Sends to example.com
      server | rtail --port 43567         Uses custom port
      server | rtail --mute               No stdout
      server | rtail --no-tty             Strips ansi colors
      server | rtail --no-date-parse      Disable date parsing/stripping


## `rtail-server(1)`

`rtail-server` receives all messages broadcast from every `rtail` client, displaying all incoming log streams in a realtime web view. **Under the hood, the server uses [socket.io](http://socket.io) to pipe every incoming UDP message to the browser.**

There is little to no configuration – The default UDP/HTTP ports can be changed, but that's it.

## Examples

Use default values:

    $ rtail-server

Always use latest, stable webapp:

    $ rtail-server --web-version stable

Use custom ports:

    $ rtail-server --web-port 8080 --udp-port 9090

Set debugging on:

    $ DEBUG=rtail:* rtail-server

Open your browser and start tailing logs!

## Params

    $ rtail-server --help
    Usage: rtail-server [OPTIONS]

    Options:
    --udp-host, --uh  The listening UDP hostname            [default: "127.0.0.1"]
    --udp-port, --up  The listening UDP port                       [default: 9999]
    --web-host, --wh  The listening HTTP hostname           [default: "127.0.0.1"]
    --web-port, --wp  The listening HTTP port                      [default: 8888]
    --web-version     Define web app version to serve                     [string]
    --backlog, -b     Lines of history kept per stream    [number] [default: 100]
    --help, -h        Show help                                          [boolean]
    --version, -v     Show version number                                [boolean]

Every option can also be set as an environment variable, prefixed with
`RTAIL_` — `RTAIL_WEB_PORT=8080`, `RTAIL_BACKLOG=500`, and so on. This is how
the container image is configured.

    Examples:
    rtail-server --web-port 8080         Use custom HTTP port
    rtail-server --udp-port 8080         Use custom UDP port
    rtail-server --web-version stable    Always uses latest stable webapp
    rtail-server --web-version unstable  Always uses latest unreleased webapp
    rtail-server --web-version 0.1.3     Use webapp v0.1.3

## UDP Broadcasting

To scale and broadcast on multiple servers, instruct the `rtail` client to stream to the broadcast address. Every message will then be delivered to all servers in your subnet.

## Authentication layer

For the time being, the webapp doesn't have an authentication layer; it assumes that you will run it behind a VPN or reverse proxy, with a simple `Authorization` header check.

# Running it locally

Note there are two images, and they are not the same thing:
[`Dockerfile`](Dockerfile) is the server you deploy, and
[`tools/Dockerfile`](tools/Dockerfile) below is the development toolchain,
which mounts your checkout.

The toolchain lives in a container ([`tools/Dockerfile`](tools/Dockerfile)), so Docker is
the only thing you need installed — no Node.js, no npm, no global CLIs.

    $ make dev

That builds the assets, starts `rtail-server`, feeds it three live demo
streams, and serves the webapp. It prints the URL — `make url` prints it
again. Stylesheets recompile on save; reload the browser to pick them up.
`Ctrl-C` stops everything.

The first run also builds the toolchain image and installs dependencies, so it
takes a minute; subsequent runs start immediately.

To leave it running in the background instead:

    $ make up         # start detached, wait for it, print the URL
    $ make logs       # tail it
    $ make down       # stop it

Other targets:

    $ make build      # build the webapp into app/
    $ make dist       # build the minified webapp into dist/
    $ make test       # run the test suite
    $ make typecheck  # type-check the webapp
    $ make shell      # open a shell inside the toolchain container
    $ make clean      # remove generated assets and dependencies

### Ports

Ports are derived from the worktree path, so several checkouts of this repo can
run at once without colliding. Each gets its own HTTP port, UDP port, and
container name, fixed across runs — sharing any of the three fails confusingly,
since two servers can both bind the same UDP port and the loser simply never
receives a log line.

`make url` reports the current one. Override with `make dev PORT=9000
UDP_PORT=9001`.

If you do have a Node.js toolchain on your machine, the same targets are plain
npm scripts — `npm install && npm run dev`.

### The stack

| | |
| --- | --- |
| CLI | ESM, Node ≥ 20, [yargs](https://yargs.js.org), [socket.io](https://socket.io) |
| Webapp | [Preact](https://preactjs.com) + TypeScript, ~33 KB gzipped |
| Build | [esbuild](https://esbuild.github.io) + [dart-sass](https://sass-lang.com) |
| Tests | the built-in `node:test` runner |

The webapp has no framework runtime beyond Preact: routing, preferences,
popovers, and timestamp formatting are a few dozen lines each over the
platform (`history`, `localStorage`, `Intl`) rather than dependencies.

# How to contribute

`main` is the only long-lived branch, and it is always releasable.

* Branch off `main`, and open a pull request back into `main`
* Keep the branch short-lived; there is no `develop` or release branch to merge through
* CI must be green before a pull request lands

The test suite runs on the built-in [`node:test`](https://nodejs.org/api/test.html)
runner. Use the tests to check whether your contribution breaks some part of the
library, and be sure to add new tests for each new feature.

    $ make test

The webapp is TypeScript; please keep it type-clean:

    $ make typecheck

CI runs both, plus the production build, on Node 20 and 22.

## Releasing

The `version` field in `package.json` is the release trigger. Bump it in a pull
request like any other change; when that pull request lands on `main`, CI
re-runs the type-check, build, and tests, then tags the commit `vX.Y.Z`, cuts a
GitHub release with generated notes, and pushes the multi-arch container image
to `ghcr.io/kilianc/rtail`.

A version with a pre-release suffix (`0.3.0-rc.1`) is marked as a pre-release
and is never tagged `latest`. Landing anything else on `main` releases nothing.

## Contributors

* [Kilian Ciuffolo](https://github.com/kilianc)
* [Luca Orio](https://www.behance.net/lucaorio)
* [Sandaruwan Silva](https://github.com/s-silva)
* [Sorel Mihai](https://dribbble.com/sorelmihai)
* [Tim Riot](https://www.linkedin.com/in/timriot)

## Roadmap (aka where you can help)

* Optional HTTP basic auth on the web port, and a shared secret on UDP ingest.
  Full OAuth ([#44](https://github.com/kilianc/rtail/issues/44)) needs a user
  model and session storage, which is a lot of machinery for a tool with no
  persistence layer — basic auth covers the actual risk.
* A catch-all docker logs image: watch `docker events`, pipe every container's
  `docker logs -f` into a stream named after it.
* Broaden the test suite, particularly around the UDP ingest path.

Dropped, and why:

* ~~Rewrite webapp using ng2~~ — done differently. The webapp is now Preact +
  TypeScript; Angular 2 was already obsolete by the time it came up.
* ~~Publish base rtail docker image to DockerHub~~ — done, but to GHCR rather
  than DockerHub, since it needs no separate account or secrets.
* ~~Write a rock solid test suite~~ — a suite exists and runs in CI. Deepening
  it is the open item above.
* ~~Allow use of DTLS~~ — the linked PR was against `joyent/node`, a repo that
  no longer exists, and Node core still has no DTLS. Run rtail over WireGuard
  or Tailscale instead; that is the modern answer to this problem.
* ~~Infinite-scroll in the webapp~~ — cannot be built as stated. The server
  keeps a fixed-size ring buffer per stream and sends it in one message; there
  is no pagination to scroll through. Real history means a persistence layer,
  which contradicts the design. `--backlog` covers the useful part.

## Sponsors
❤ rTail? Consider sponsoring this project to keep it alive and free for the community.

* ? (wildcard TLS cert)
* ? (.io domain)

[![PayPal donate button](https://img.shields.io/badge/$_paypal-one_time_donation_➝-04cd7e.svg?style=flat-square)](https://www.paypal.com/cgi-bin/webscr?cmd=_donations&business=info%40rtail%2eorg&lc=US&item_name=rtail&item_number=rtail&currency_code=USD&bn=PP%2dDonationsBF%3abtn_donateCC_LG%2egif%3aNonHosted)

## License

_This software is released under the MIT license cited below_.

    Copyright (c) 2014 Kilian Ciuffolo, me@nailik.org. All Rights Reserved.

    Permission is hereby granted, free of charge, to any person
    obtaining a copy of this software and associated documentation
    files (the 'Software'), to deal in the Software without
    restriction, including without limitation the rights to use,
    copy, modify, merge, publish, distribute, sublicense, and/or sell
    copies of the Software, and to permit persons to whom the
    Software is furnished to do so, subject to the following
    conditions:

    The above copyright notice and this permission notice shall be
    included in all copies or substantial portions of the Software.

    THE SOFTWARE IS PROVIDED 'AS IS', WITHOUT WARRANTY OF ANY KIND,
    EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES
    OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
    NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT
    HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
    WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
    FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR
    OTHER DEALINGS IN THE SOFTWARE.
