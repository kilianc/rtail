package rql

import "testing"

// Each of these is what DuckDB 1.4's `->>` returned for the number on the
// left; see internal/query's agreement test for the same against DuckDB live.
func TestJSONNumbersAreWrittenAsDuckDBWritesThem(t *testing.T) {
	for text, want := range map[string]string{
		"100":                    "100",
		"-0":                     "0",
		"123456789012345678":     "123456789012345678",
		"18446744073709551615":   "18446744073709551615",
		"18446744073709551616":   "18446744073709551616",
		"-9223372036854775809":   "-9223372036854775809",
		"100000000000000000000":  "100000000000000000000",
		"1.0":                    "1.0",
		"1.50":                   "1.5",
		"1e2":                    "100.0",
		"1E2":                    "100.0",
		"1.0E+2":                 "100.0",
		"-1.5":                   "-1.5",
		"0.0":                    "0.0",
		"-0.0":                   "-0.0",
		"0.1":                    "0.1",
		"0.30000000000000004":    "0.30000000000000004",
		"123e-2":                 "1.23",
		"2.5e+10":                "25000000000.0",
		"1e15":                   "1000000000000000.0",
		"9007199254740993.0":     "9007199254740992.0",
		"12345678901234567890.0": "12345678901234567000.0",
		"1e20":                   "100000000000000000000.0",
		"9.999999999999999e20":   "999999999999999900000.0",
		"1e21":                   "1e21",
		"1.5e21":                 "1.5e21",
		"1e300":                  "1e300",
		"1e-5":                   "0.00001",
		"1e-6":                   "0.000001",
		"1.5e-6":                 "0.0000015",
		"1e-7":                   "1e-7",
		"1.234e-7":               "1.234e-7",
		"1.2345678901234567e-5":  "0.000012345678901234568",
		"5e-324":                 "5e-324",
		"1.7976931348623157e308": "1.7976931348623157e308",
		"1e-400":                 "0.0",
		"1e400":                  "1e400",
		"-1e400":                 "-1e400",
	} {
		if got := jsonNumber(text); want != got {
			t.Errorf("jsonNumber(%s) = %s, want %s", text, got, want)
		}
	}
}

func TestJSONContainersAreWrittenAsDuckDBWritesThem(t *testing.T) {
	for document, want := range map[string]string{
		`{"b":1,"a":"<&>"}`:                  `{"b":1,"a":"<&>"}`,
		`[1, 2.50, 1e2, -0]`:                 `[1,2.5,100.0,0]`,
		`{ "a" : [ ] , "b" : { } }`:          `{"a":[],"b":{}}`,
		`{"a":1,"a":2}`:                      `{"a":1,"a":2}`,
		`{"n":null,"t":true,"f":false}`:      `{"n":null,"t":true,"f":false}`,
		`{"s":"a\u00e9\u2028/"}`:             "{\"s\":\"a\u00e9\u2028/\"}",
		`{"s":"\b\f\n\r\t\u0001\u001f\"\\"}`: `{"s":"\b\f\n\r\t\u0001\u001F\"\\"}`,
	} {
		value, ok := jsonValue([]byte(document))
		if !ok || want != value.Str {
			t.Errorf("jsonValue(%s) = %s, want %s", document, value.Str, want)
		}
	}
}

// A repeated key is the first one, as DuckDB's lookup finds it.
func TestARepeatedKeyIsTheFirst(t *testing.T) {
	value, ok := jsonPath(`{"o":{"k":"first","k":"second"}}`, "o.k")
	if !ok || "first" != value.Str {
		t.Errorf("o.k = %q, want first", value.Str)
	}
}
