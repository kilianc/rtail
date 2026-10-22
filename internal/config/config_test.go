package config

import (
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
	"time"
)

func parse(t *testing.T, args ...string) *Config {
	t.Helper()

	cfg, err := Parse(args, "test", io.Discard)
	if nil != err {
		t.Fatalf("Parse(%v): %v", args, err)
	}

	return cfg
}

func TestDefaultsMatchV1(t *testing.T) {
	cfg := parse(t)

	if "127.0.0.1" != cfg.UDPHost || 9999 != cfg.UDPPort {
		t.Errorf("udp = %s:%d, want 127.0.0.1:9999", cfg.UDPHost, cfg.UDPPort)
	}
	if "127.0.0.1" != cfg.WebHost || 8888 != cfg.WebPort {
		t.Errorf("web = %s:%d, want 127.0.0.1:8888", cfg.WebHost, cfg.WebPort)
	}
	if 100 != cfg.Backlog {
		t.Errorf("backlog = %d, want 100", cfg.Backlog)
	}
}

// Every docker-compose.yml in the wild sets these.
func TestEnvironmentIsHonoured(t *testing.T) {
	t.Setenv("RTAIL_WEB_PORT", "8080")
	t.Setenv("RTAIL_UDP_HOST", "0.0.0.0")
	t.Setenv("RTAIL_BACKLOG", "500")
	t.Setenv("RTAIL_VERBOSE", "true")

	cfg := parse(t)

	if 8080 != cfg.WebPort {
		t.Errorf("web-port = %d, want 8080", cfg.WebPort)
	}
	if "0.0.0.0" != cfg.UDPHost {
		t.Errorf("udp-host = %q, want 0.0.0.0", cfg.UDPHost)
	}
	if 500 != cfg.Backlog {
		t.Errorf("backlog = %d, want 500", cfg.Backlog)
	}
	if !cfg.Verbose {
		t.Error("verbose = false, want true")
	}
}

func TestFlagsBeatTheEnvironment(t *testing.T) {
	t.Setenv("RTAIL_WEB_PORT", "8080")

	cfg := parse(t, "--web-port", "9090")

	if 9090 != cfg.WebPort {
		t.Errorf("web-port = %d, want the flag's 9090", cfg.WebPort)
	}
}

// The v1 short aliases are documented and in people's scripts.
//
// --insecure because the point here is the aliases, not the safety rule: a
// non-loopback --wh with no token is refused, which TestRefusesToBeOpenOffLoopback
// covers on its own.
func TestV1ShortAliases(t *testing.T) {
	cfg := parse(t, "--uh", "1.2.3.4", "--up", "1111", "--wh", "5.6.7.8", "--wp", "2222", "-b", "7", "--insecure")

	if "1.2.3.4" != cfg.UDPHost || 1111 != cfg.UDPPort {
		t.Errorf("udp = %s:%d", cfg.UDPHost, cfg.UDPPort)
	}
	if "5.6.7.8" != cfg.WebHost || 2222 != cfg.WebPort {
		t.Errorf("web = %s:%d", cfg.WebHost, cfg.WebPort)
	}
	if 7 != cfg.Backlog {
		t.Errorf("backlog = %d, want 7", cfg.Backlog)
	}
}

func TestInvalidValuesAreRejected(t *testing.T) {
	cases := [][]string{
		{"--backlog", "0"},
		{"--backlog", "-5"},
		{"--web-port", "70000"},
		{"--udp-port", "-1"},
	}

	for _, args := range cases {
		if _, err := Parse(args, "test", io.Discard); nil == err {
			t.Errorf("Parse(%v) was accepted", args)
		}
	}
}

/*!
 * A malformed environment value is an error naming the variable.
 *
 * Falling back to the default looked kind and was not: RTAIL_RETENTION="30
 * days" became "keep everything forever", and RTAIL_WEB_PORT=80a became 8888,
 * and nobody was told either.
 */
func TestMalformedEnvironmentIsAnError(t *testing.T) {
	for name, value := range map[string]string{
		"RTAIL_WEB_PORT":  "80a",
		"RTAIL_RETENTION": "30 days",
		"RTAIL_INSECURE":  "yes please",
		"RTAIL_BACKLOG":   "lots",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)

			_, err := Parse(nil, "test", io.Discard)
			if nil == err {
				t.Fatalf("%s=%q was accepted", name, value)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("the error should name %s, got: %v", name, err)
			}
		})
	}
}

/*!
 * Durations that would do the opposite of what was meant.
 *
 * A negative retention is a cutoff in the future, and the compactor deletes
 * every file older than the cutoff — which is all of them.
 */
func TestNegativeDurationsAreRefused(t *testing.T) {
	for _, flag := range []string{"--retention", "--retention-raw", "--downsample-after", "--compact-interval"} {
		if _, err := Parse([]string{flag, "-30d"}, "test", io.Discard); nil == err {
			t.Errorf("%s -30d was accepted", flag)
		} else if !strings.Contains(err.Error(), flag) {
			t.Errorf("%s: the error should name the flag, got: %v", flag, err)
		}
	}

	t.Setenv("RTAIL_RETENTION", "-1w")
	if _, err := Parse(nil, "test", io.Discard); nil == err {
		t.Error("RTAIL_RETENTION=-1w was accepted")
	}
}

func TestBodyLimitMustBePositive(t *testing.T) {
	for _, value := range []string{"0", "-1"} {
		if _, err := Parse([]string{"--max-body-mb", value}, "test", io.Discard); nil == err {
			t.Errorf("--max-body-mb %s was accepted", value)
		}
	}
}

func TestDurationsThatOverflowAreRejected(t *testing.T) {
	for _, input := range []string{"1e10w", "-1e300d"} {
		if _, err := ParseDuration(input); nil == err {
			t.Errorf("ParseDuration(%q) was accepted", input)
		}
	}
}

// The level is canonicalised the way ingest stores it, and a name nobody has
// heard of is refused rather than ranked as "unknown".
func TestDownsampleLevelIsALevel(t *testing.T) {
	if cfg := parse(t, "--downsample-level", "warning", "--downsample-after", "14d"); "WARN" != cfg.DownsampleLevel {
		t.Errorf("downsample-level = %q, want WARN", cfg.DownsampleLevel)
	}
	if cfg := parse(t); "WARN" != cfg.DownsampleLevel {
		t.Errorf("default downsample-level = %q, want WARN", cfg.DownsampleLevel)
	}
	if _, err := Parse([]string{"--downsample-level", "LOUD"}, "test", io.Discard); nil == err {
		t.Error("--downsample-level LOUD was accepted")
	}
	if _, err := Parse([]string{"--downsample-level", "", "--downsample-after", "14d"}, "test", io.Discard); nil == err {
		t.Error("an empty level with --downsample-after set was accepted")
	}
}

// A token of spaces is no token: auth.New trims it away, so it must not count
// as one when deciding whether an open address is allowed.
func TestAWhitespaceTokenIsNoToken(t *testing.T) {
	if _, err := Parse([]string{"--web-host", "0.0.0.0", "--auth-token", "   "}, "test", io.Discard); nil == err {
		t.Error("a whitespace-only token allowed binding 0.0.0.0")
	}

	cfg := parse(t, "--web-host", "0.0.0.0", "--auth-token", " s3cret\n")
	if "s3cret" != cfg.AuthToken {
		t.Errorf("token = %q, want it trimmed as the guard trims it", cfg.AuthToken)
	}
}

func TestVersionPrintsAndStops(t *testing.T) {
	var out strings.Builder

	_, err := Parse([]string{"--version"}, "2.0.0-test", &out)

	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
	if "2.0.0-test" != strings.TrimSpace(out.String()) {
		t.Errorf("output = %q", out.String())
	}
}

func TestUnknownFlagIsAnError(t *testing.T) {
	if _, err := Parse([]string{"--web-version", "stable"}, "test", io.Discard); nil == err {
		t.Error("the removed --web-version flag was accepted")
	}
}

func TestParseDurationAcceptsDaysAndWeeks(t *testing.T) {
	cases := []struct {
		input string
		want  time.Duration
	}{
		{"30d", 30 * 24 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
		{"0.5d", 12 * time.Hour},
		{"12h", 12 * time.Hour},
		{"90m", 90 * time.Minute},
		{"", 0},
		// Signed, as the search API's relative from/to are.
		{"-7d", -7 * 24 * time.Hour},
		{"+1w", 7 * 24 * time.Hour},
	}

	for _, tc := range cases {
		got, err := ParseDuration(tc.input)
		if nil != err {
			t.Errorf("ParseDuration(%q): %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseDuration(%q) = %s, want %s", tc.input, got, tc.want)
		}
	}
}

func TestParseDurationRejectsNonsense(t *testing.T) {
	for _, input := range []string{"soon", "30x", "d", "--", "7dw", "Infd", "-NaNw"} {
		if _, err := ParseDuration(input); nil == err {
			t.Errorf("ParseDuration(%q) was accepted", input)
		}
	}
}

func TestRetentionFlagsParse(t *testing.T) {
	cfg, err := Parse([]string{
		"--retention", "30d",
		"--retention-raw", "7d",
		"--compact-interval", "90s",
		"--downsample-after", "2w",
	}, "test", io.Discard)
	if nil != err {
		t.Fatal(err)
	}

	if 30*24*time.Hour != cfg.Retention {
		t.Errorf("retention = %s", cfg.Retention)
	}
	if 7*24*time.Hour != cfg.RetentionRaw {
		t.Errorf("retention-raw = %s", cfg.RetentionRaw)
	}
	if 90*time.Second != cfg.CompactInterval {
		t.Errorf("compact-interval = %s", cfg.CompactInterval)
	}
	if 14*24*time.Hour != cfg.DownsampleAfter {
		t.Errorf("downsample-after = %s", cfg.DownsampleAfter)
	}
}

// Compaction defaults to on; only an explicit 0 disables it.
func TestCompactionDefaultsOn(t *testing.T) {
	cfg, err := Parse(nil, "test", io.Discard)
	if nil != err {
		t.Fatal(err)
	}
	if cfg.CompactInterval <= 0 {
		t.Errorf("compact-interval = %s, want a positive default", cfg.CompactInterval)
	}

	off, err := Parse([]string{"--compact-interval", "0"}, "test", io.Discard)
	if nil != err {
		t.Fatal(err)
	}
	if 0 != off.CompactInterval {
		t.Errorf("compact-interval = %s, want 0", off.CompactInterval)
	}
}

/*!
 * The one rule that is about safety rather than typos.
 *
 * A server bound where other machines can reach it, with no token, is an
 * open archive of everything the application has printed. It has to be
 * impossible to do that silently — so it is refused, and sayable two ways:
 * configure a token, or state that something in front handles access.
 */
func TestRefusesToBeOpenOffLoopback(t *testing.T) {
	_, err := Parse([]string{"--web-host", "0.0.0.0"}, "test", io.Discard)
	if nil == err {
		t.Fatal("binding 0.0.0.0 with no token should be refused")
	}
	if !strings.Contains(err.Error(), "--auth-token") {
		t.Errorf("the error should say how to fix it, got: %v", err)
	}
}

func TestLoopbackNeedsNoToken(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "::1"} {
		if _, err := Parse([]string{"--web-host", host}, "test", io.Discard); nil != err {
			t.Errorf("--web-host %s should need no token: %v", host, err)
		}
	}
}

func TestATokenOrInsecureAllowsAnyAddress(t *testing.T) {
	if _, err := Parse([]string{"--web-host", "0.0.0.0", "--auth-token", "s3cret"}, "test", io.Discard); nil != err {
		t.Errorf("a token should allow any address: %v", err)
	}
	if _, err := Parse([]string{"--web-host", "0.0.0.0", "--insecure"}, "test", io.Discard); nil != err {
		t.Errorf("--insecure should allow any address: %v", err)
	}
}

// Half a TLS pair is a misconfiguration that would otherwise only show up as
// a handshake failure on somebody's first request.
func TestTLSNeedsBothHalves(t *testing.T) {
	if _, err := Parse([]string{"--tls-cert", "cert.pem"}, "test", io.Discard); nil == err {
		t.Error("--tls-cert alone should be refused")
	}
	if _, err := Parse([]string{"--tls-key", "key.pem"}, "test", io.Discard); nil == err {
		t.Error("--tls-key alone should be refused")
	}
}
