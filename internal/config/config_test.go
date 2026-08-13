package config

import (
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
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
func TestV1ShortAliases(t *testing.T) {
	cfg := parse(t, "--uh", "1.2.3.4", "--up", "1111", "--wh", "5.6.7.8", "--wp", "2222", "-b", "7")

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

// A malformed environment value falls back to the default rather than failing
// to boot — a container that will not start because of a stray quote in a
// compose file is worse than one that ignores it.
func TestMalformedEnvironmentFallsBack(t *testing.T) {
	t.Setenv("RTAIL_WEB_PORT", "not a number")

	if cfg := parse(t); 8888 != cfg.WebPort {
		t.Errorf("web-port = %d, want the default 8888", cfg.WebPort)
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
