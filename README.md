# `r-tail`

### Fork original rtail(https://github.com/kilianc/rtail) with fixes and improvments

## Terminal output to the browser in seconds, using UNIX pipes.

`rtail` is a command line utility that grabs every line in `stdin` and broadcasts it over **UDP**. That's it. Nothing fancy. Nothing complicated. Tail log files, app output, or whatever you wish, using `rtail` broadcasting to an `rtail-server` – See multiple streams in the browser, in realtime.

## Installation

  $ npm install -g r-tail

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
      --id, --name   The log stream id                 [string] [default: (default)]
      --group, -g    The log stream group name              [string] [default: null]
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

## `rtail-server`

`rtail-server` receives all messages broadcast from every `rtail` client, displaying all incoming log streams in a realtime web view. **Under the hood, the server uses [socket.io](http://socket.io) to pipe every incoming UDP message to the browser.**

There is little to no configuration – The default UDP/HTTP ports can be changed, but that's it.

## Examples

Use default values:

    $ rtail-server

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
    --help, -h        Show help                                          [boolean]
    --version, -v     Show version number                                [boolean]

    Examples:
    rtail-server --web-port 8080         Use custom HTTP port
    rtail-server --udp-port 8080         Use custom UDP port

## UDP Broadcasting

To scale and broadcast on multiple servers, instruct the `rtail` client to stream to the broadcast address. Every message will then be delivered to all servers in your subnet.

## Authentication layer

For the time being, the webapp doesn't have an authentication layer; it assumes that you will run it behind a VPN or reverse proxy, with a simple `Authorization` header check.

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
