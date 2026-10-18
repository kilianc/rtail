package client

import (
	"fmt"
	"strconv"
	"strings"
)

/*!
 * The flags, parsed by hand.
 *
 * `flag` from the standard library cannot express this surface: `-h` is bound
 * to --host rather than to help, booleans take a `--no-` negation, and an
 * unknown flag has to be an error rather than the start of the positional
 * arguments. Those are yargs behaviours that the v1 client shipped and that
 * somebody's pipeline is relying on, so they are kept and this is sixty lines
 * rather than a dependency.
 */

type options struct {
	Host    string
	Port    int
	ID      string
	Mute    bool
	TTY     bool
	Help    bool
	Version bool
}

const usage = `Usage: cmd | rtail [OPTIONS]

Options:
  --host, -h     The server host                 [string] [default: "127.0.0.1"]
  --port, -p     The server port                        [number] [default: 9999]
  --id, --name   The log stream id                 [string] [default: (moniker)]
  --mute, -m     Don't pipe stdin with stdout                          [boolean]
  --tty          Keeps ansi colors                     [boolean] [default: true]
  --help         Show help                                             [boolean]
  --version, -v  Show version number                                   [boolean]

Examples:
  server | rtail > server.log         localhost + file
  server | rtail --id api.domain.com  Name the log stream
  server | rtail --host example.com   Sends to example.com
  server | rtail --port 43567         Uses custom port
  server | rtail --mute               No stdout
  server | rtail --no-tty             Strips ansi colors
`

func parseArgs(args []string) (options, error) {
	opts := options{Host: "127.0.0.1", Port: 9999, ID: moniker(), TTY: true}

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// --flag=value and --flag value are the same thing.
		name, inline, hasInline := strings.Cut(arg, "=")

		// `next` consumes the following argument for a flag that takes one.
		next := func() (string, error) {
			if hasInline {
				return inline, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("not enough arguments following: %s", name)
			}
			i++
			return args[i], nil
		}

		// A boolean is set by being present, but `--tty=false` has to mean
		// what it says rather than being read as presence and a discarded
		// word after the sign.
		flagOn := func(negated bool) (bool, error) {
			if !hasInline {
				return !negated, nil
			}
			value, err := strconv.ParseBool(inline)
			if nil != err {
				return false, fmt.Errorf("%s is not a boolean: %s", name, inline)
			}
			if negated {
				return !value, nil
			}
			return value, nil
		}

		var err error

		switch name {
		case "--host", "-h":
			opts.Host, err = next()
		case "--port", "-p":
			var raw string
			if raw, err = next(); nil == err {
				if opts.Port, err = strconv.Atoi(raw); nil != err {
					err = fmt.Errorf("port is not a number: %s", raw)
				}
			}
		case "--id", "--name":
			opts.ID, err = next()
		case "--mute", "-m":
			opts.Mute, err = flagOn(false)
		case "--no-mute":
			opts.Mute, err = flagOn(true)
		case "--tty":
			opts.TTY, err = flagOn(false)
		case "--no-tty":
			opts.TTY, err = flagOn(true)
		case "--help":
			opts.Help, err = flagOn(false)
		case "--version", "-v":
			opts.Version, err = flagOn(false)
		default:
			// Strict, like yargs' .strict(): a typo in a flag name is a
			// mistake worth stopping for, not something to pass through.
			return opts, fmt.Errorf("unknown argument: %s", name)
		}

		if nil != err {
			return opts, err
		}
	}

	if "" == opts.ID {
		return opts, fmt.Errorf("--id cannot be empty")
	}

	if 1 > opts.Port || 65535 < opts.Port {
		return opts, fmt.Errorf("port out of range: %d", opts.Port)
	}

	return opts, nil
}
