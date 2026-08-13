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
	"time"
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

	// Compaction and retention. All durations accept a `d` or `w` suffix as
	// well as Go's own units — log retention is naturally expressed in days,
	// and `--retention 720h` is nobody's idea of clear.
	CompactInterval time.Duration
	ClusterBy       string
	Retention       time.Duration
	RetentionRaw    time.Duration
	DownsampleAfter time.Duration
	DownsampleLevel string

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

	dur := func(field *time.Duration, fallback time.Duration, long, usage string) {
		value := envDuration(long, fallback)
		set.Func(long, usage+" (default "+value.String()+")", func(raw string) error {
			parsed, err := ParseDuration(raw)
			if nil != err {
				return err
			}
			*field = parsed
			return nil
		})
		*field = value
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

	dur(&cfg.CompactInterval, 5*time.Minute, "compact-interval", "how often to compact; 0 disables it")
	str(&cfg.ClusterBy, "level", "cluster-by", "", "leading sort column for daily files (empty: time only)")
	dur(&cfg.Retention, 0, "retention", "delete data older than this (unset: keep forever)")
	dur(&cfg.RetentionRaw, 0, "retention-raw", "drop the original line after this, keeping the columns")
	dur(&cfg.DownsampleAfter, 0, "downsample-after", "discard low-severity records older than this")
	str(&cfg.DownsampleLevel, "WARN", "downsample-level", "", "lowest severity kept by --downsample-after")

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

/*!
 * ParseDuration is time.ParseDuration plus `d` and `w`.
 *
 * Retention is naturally expressed in days and weeks, and making an operator
 * write `--retention 720h` for a month is the kind of small hostility that
 * makes a tool feel unfinished. Go's own parser has no unit longer than an
 * hour because days are ambiguous under daylight saving; that ambiguity does
 * not matter for "delete things older than about a month", which is the only
 * thing this is used for.
 */
func ParseDuration(value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if "" == trimmed {
		return 0, nil
	}

	multiplier := time.Duration(0)

	switch {
	case strings.HasSuffix(trimmed, "d"):
		multiplier = 24 * time.Hour
	case strings.HasSuffix(trimmed, "w"):
		multiplier = 7 * 24 * time.Hour
	}

	if 0 != multiplier {
		count, err := strconv.ParseFloat(strings.TrimRight(trimmed, "dw"), 64)
		if nil != err {
			return 0, fmt.Errorf("bad duration %q", value)
		}
		return time.Duration(count * float64(multiplier)), nil
	}

	parsed, err := time.ParseDuration(trimmed)
	if nil != err {
		return 0, fmt.Errorf("bad duration %q (try 30d, 12h, 90m)", value)
	}

	return parsed, nil
}

func envDuration(name string, fallback time.Duration) time.Duration {
	if value, ok := os.LookupEnv(envKey(name)); ok {
		if parsed, err := ParseDuration(value); nil == err {
			return parsed
		}
	}
	return fallback
}
