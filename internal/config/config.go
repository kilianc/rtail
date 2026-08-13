/*!
 * Command line and environment configuration.
 *
 * Flag names, short aliases and RTAIL_* environment variables all match v1
 * exactly, because the container image and every docker-compose.yml in the
 * wild sets them. Precedence is flag > environment > default, which is what
 * yargs did and what people expect.
 */

package config

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Config is the server's runtime configuration.
type Config struct {
	UDPHost string
	UDPPort int
	WebHost string
	WebPort int
	Backlog int

	// DataDir is where the durable store will live. Empty means memory-only,
	// which is all P0 implements and remains the zero-configuration mode.
	DataDir string

	// WebRoot serves the webapp from a directory instead of the copy embedded
	// in the binary. This is what the asset watcher needs during development,
	// and it replaces v1's --web-version development mode.
	WebRoot string

	Verbose bool
}

// Parse builds a Config from arguments and the environment.
func Parse(args []string, version string, out io.Writer) (*Config, error) {
	cfg := &Config{}

	set := flag.NewFlagSet("rtail-server", flag.ContinueOnError)
	set.SetOutput(out)

	var showVersion bool

	// Each option is registered under its long name and its v1 short alias,
	// both pointing at the same field, so either spelling works.
	str := func(field *string, fallback, long, short, usage string) {
		value := env(long, fallback)
		set.StringVar(field, long, value, usage)
		if "" != short {
			set.StringVar(field, short, value, "alias for --"+long)
		}
	}

	num := func(field *int, fallback int, long, short, usage string) {
		value := envInt(long, fallback)
		set.IntVar(field, long, value, usage)
		if "" != short {
			set.IntVar(field, short, value, "alias for --"+long)
		}
	}

	str(&cfg.UDPHost, "127.0.0.1", "udp-host", "uh", "the listening UDP hostname")
	num(&cfg.UDPPort, 9999, "udp-port", "up", "the listening UDP port")
	str(&cfg.WebHost, "127.0.0.1", "web-host", "wh", "the listening HTTP hostname")
	num(&cfg.WebPort, 8888, "web-port", "wp", "the listening HTTP port")
	num(&cfg.Backlog, 100, "backlog", "b", "lines of history kept per stream")
	str(&cfg.DataDir, "", "data", "", "directory for durable storage (unset: memory only)")
	str(&cfg.WebRoot, "", "web-root", "", "serve the webapp from this directory instead of the binary")

	set.BoolVar(&cfg.Verbose, "verbose", envBool("verbose", false), "log at debug level")
	set.BoolVar(&showVersion, "version", false, "print the version and exit")
	set.BoolVar(&showVersion, "v", false, "alias for --version")

	set.Usage = func() {
		fmt.Fprintf(out, "Usage: rtail-server [OPTIONS]\n\n")
		fmt.Fprintf(out, "Every option is also settable as RTAIL_*, e.g. RTAIL_WEB_PORT.\n\n")
		set.PrintDefaults()
	}

	if err := set.Parse(args); nil != err {
		return nil, err
	}

	if showVersion {
		fmt.Fprintln(out, version)
		return nil, flag.ErrHelp
	}

	if cfg.Backlog < 1 {
		return nil, fmt.Errorf("--backlog must be a positive integer")
	}

	if cfg.UDPPort < 0 || cfg.UDPPort > 65535 || cfg.WebPort < 0 || cfg.WebPort > 65535 {
		return nil, fmt.Errorf("ports must be between 0 and 65535")
	}

	return cfg, nil
}

// envKey maps a flag name onto its environment variable: udp-host -> RTAIL_UDP_HOST.
func envKey(name string) string {
	return "RTAIL_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

func env(name, fallback string) string {
	if value, ok := os.LookupEnv(envKey(name)); ok {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if value, ok := os.LookupEnv(envKey(name)); ok {
		if n, err := strconv.Atoi(value); nil == err {
			return n
		}
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	if value, ok := os.LookupEnv(envKey(name)); ok {
		if b, err := strconv.ParseBool(value); nil == err {
			return b
		}
	}
	return fallback
}
