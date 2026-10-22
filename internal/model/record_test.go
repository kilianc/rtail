package model

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// A NaN or an Infinity is a legal OTLP double and has no JSON spelling. One
// such field must not make the record — and so the page or the tail backlog
// it travels in — impossible to encode.
func TestNonFiniteFloatsEncodeAsNull(t *testing.T) {
	rec := &Record{
		Ts:     time.UnixMilli(1),
		Stream: "s",
		Raw:    "x",
		Type:   "string",
		Fields: map[string]Value{
			"nan":  Float(math.NaN()),
			"inf":  Float(math.Inf(1)),
			"-inf": Float(math.Inf(-1)),
			"ok":   Float(1.5),
		},
	}

	body, err := json.Marshal([]*Record{rec})
	if nil != err {
		t.Fatalf("encoding: %v", err)
	}

	var decoded []struct {
		Fields map[string]any `json:"fields"`
	}
	if err := json.Unmarshal(body, &decoded); nil != err {
		t.Fatalf("decoding %s: %v", body, err)
	}

	fields := decoded[0].Fields
	for _, key := range []string{"nan", "inf", "-inf"} {
		if value, present := fields[key]; !present || nil != value {
			t.Errorf("%s = %v (present %v), want null", key, value, present)
		}
	}
	if 1.5 != fields["ok"] {
		t.Errorf("ok = %v, want 1.5", fields["ok"])
	}
}
