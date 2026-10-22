/*!
 * rtail — the pipe.
 *
 *     server | rtail --id api.example.com
 *
 * Reads stdin a line at a time, echoes it to stdout, and sends each line to
 * an rtail server as a UDP datagram. The work is in internal/client; this is
 * the shell around it, so that everything with behaviour can be tested
 * without starting a process.
 */
package main

import (
	"os"
	"time"

	"github.com/kilianc/rtail/v2/internal/client"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "2.0.0-dev"

func main() {
	// Colour is kept only for a real terminal. A pipe or a file gets the text
	// without escapes, which is what makes `cmd | rtail > server.log` produce
	// a readable log rather than one full of \x1b[32m.
	info, err := os.Stdout.Stat()
	colour := nil == err && 0 != info.Mode()&os.ModeCharDevice

	os.Exit(client.Run(os.Args[1:], version, client.Env{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Colour: colour,
		Dial:   client.DialUDP,
		Now:    time.Now,
	}))
}
