package api

import (
	"testing"
	"time"
)

// The time picker offers "-7d". A relative range the UI can draw but the
// server rejects is a picker that only half works.
func TestRelativeTimesTakeDaysAndWeeks(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"-6h": 6 * time.Hour,
		"-7d": 7 * 24 * time.Hour,
		"-2w": 14 * 24 * time.Hour,
	} {
		before := time.Now().UTC()
		got, err := parseTime(value)
		if nil != err {
			t.Errorf("parseTime(%q): %v", value, err)
			continue
		}
		if ago := before.Sub(got); ago < want-time.Second || ago > want+time.Second {
			t.Errorf("parseTime(%q) is %s ago, want %s", value, ago, want)
		}
	}

	if _, err := parseTime("-soon"); nil == err {
		t.Error(`parseTime("-soon") was accepted`)
	}
}
