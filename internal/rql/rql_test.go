package rql_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/rql"
)

func parse(t *testing.T, input string) rql.Node {
	t.Helper()

	node, err := rql.Parse(input)
	if nil != err {
		t.Fatalf("Parse(%q): %v", input, err)
	}

	return node
}

/*!
 * Round-tripping is not a debug convenience — it is how the UI builds queries
 * by clicking. A "filter to this value" button appends a node and re-renders,
 * so the bar must always show something the user could have typed.
 */
func TestRoundTrip(t *testing.T) {
	cases := []struct{ input, want string }{
		{`level=ERROR`, `level=ERROR`},
		// Quoting is preserved, because it is meaningful: it suppresses value
		// coercion, so dropping it would change what the query means.
		{`level>=ERROR service="api"`, `level>=ERROR service="api"`},
		{`service=api`, `service=api`},
		{`user_id=42`, `user_id=42`},
		{`-path:/health`, `-path:/health`},
		{`NOT level=INFO`, `-level=INFO`},
		{`a=1 OR b=2`, `a=1 OR b=2`},
		{`a=1 AND b=2`, `a=1 b=2`},
		{`a=1 (b=2 OR c=3)`, `a=1 (b=2 OR c=3)`},
		{`"connection timeout"`, `"connection timeout"`},
		{`latency_ms>500`, `latency_ms>500`},
		{`trace_id=*`, `trace_id=*`},
		{`req.path:~"^/v1/"`, `req.path:~"^/v1/"`},
	}

	for _, tc := range cases {
		got := rql.String(parse(t, tc.input))
		if got != tc.want {
			t.Errorf("String(Parse(%q)) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// Re-parsing the rendering of a query must give the same query.
func TestRoundTripIsStable(t *testing.T) {
	for _, input := range []string{
		`level>=ERROR service=api "timeout"`,
		`-path:/health AND (region=us-east-1 OR region=us-west-2)`,
		`a=1 OR (b=2 c=3)`,
		`NOT (a=1 OR b=2)`,
	} {
		once := rql.String(parse(t, input))
		twice := rql.String(parse(t, once))

		if once != twice {
			t.Errorf("%q rendered to %q then %q", input, once, twice)
		}
	}
}

/*!
 * The chunk-splitting rules. These are the cases that make a search bar feel
 * broken when they are wrong, because nobody puts spaces around operators.
 */
func TestChunkSplitting(t *testing.T) {
	cases := []struct {
		input string
		field string
		op    rql.Op
		value string
	}{
		{`level>=ERROR`, "level", rql.OpGreaterEqual, "ERROR"},
		{`level<=WARN`, "level", rql.OpLessEqual, "WARN"},
		{`latency>500`, "latency", rql.OpGreater, "500"},
		// A hyphenated value must not be read as a subtraction.
		{`region=us-east-1`, "region", rql.OpEquals, "us-east-1"},
		// A path value keeps its slashes.
		{`path:/v1/orders`, "path", rql.OpContains, "/v1/orders"},
		// The regex operator wins over the substring one at the same position.
		{`msg:~^GET`, "msg", rql.OpMatches, "^GET"},
		// A dotted field is a path, not a decimal.
		{`req.path=/x`, "req.path", rql.OpEquals, "/x"},
		// A value containing an operator character splits only at the first one.
		{`url=http://x/y?a=b`, "url", rql.OpEquals, "http://x/y?a=b"},
	}

	for _, tc := range cases {
		node := parse(t, tc.input)

		comparison, ok := node.(*rql.Comparison)
		if !ok {
			t.Errorf("%q parsed to %T, want a comparison", tc.input, node)
			continue
		}

		if comparison.Field != tc.field {
			t.Errorf("%q field = %q, want %q", tc.input, comparison.Field, tc.field)
		}
		if comparison.Op != tc.op {
			t.Errorf("%q op = %v, want %v", tc.input, comparison.Op, tc.op)
		}
		if comparison.Value.Str != tc.value {
			t.Errorf("%q value = %q, want %q", tc.input, comparison.Value.Str, tc.value)
		}
	}
}

func TestValueCoercion(t *testing.T) {
	cases := []struct {
		input string
		kind  rql.Kind
	}{
		{`n=42`, rql.KindNumber},
		{`n=4.5`, rql.KindNumber},
		{`n=-3`, rql.KindNumber},
		{`b=true`, rql.KindBool},
		{`b=FALSE`, rql.KindBool},
		{`x=null`, rql.KindNull},
		{`s=hello`, rql.KindString},
		// Quoting suppresses coercion: this is the escape hatch for a field
		// whose values look like something else.
		{`s="42"`, rql.KindString},
		{`s="true"`, rql.KindString},
	}

	for _, tc := range cases {
		comparison, ok := parse(t, tc.input).(*rql.Comparison)
		if !ok {
			t.Errorf("%q did not parse to a comparison", tc.input)
			continue
		}
		if comparison.Value.Kind != tc.kind {
			t.Errorf("%q kind = %v, want %v", tc.input, comparison.Value.Kind, tc.kind)
		}
	}
}

func TestImplicitAnd(t *testing.T) {
	node := parse(t, `a=1 b=2 c=3`)

	and, ok := node.(*rql.And)
	if !ok {
		t.Fatalf("parsed to %T, want And", node)
	}
	if 3 != len(and.Children) {
		t.Errorf("children = %d, want 3", len(and.Children))
	}
}

// OR binds looser than the implicit AND.
func TestPrecedence(t *testing.T) {
	node := parse(t, `a=1 b=2 OR c=3`)

	or, ok := node.(*rql.Or)
	if !ok {
		t.Fatalf("parsed to %T, want Or at the top", node)
	}
	if 2 != len(or.Children) {
		t.Fatalf("or children = %d, want 2", len(or.Children))
	}
	if _, ok := or.Children[0].(*rql.And); !ok {
		t.Errorf("left branch = %T, want And", or.Children[0])
	}
}

func TestQuotedValuesWithSpaces(t *testing.T) {
	comparison, ok := parse(t, `msg:"connection reset by peer"`).(*rql.Comparison)
	if !ok {
		t.Fatal("did not parse to a comparison")
	}

	if "connection reset by peer" != comparison.Value.Str {
		t.Errorf("value = %q", comparison.Value.Str)
	}
	if rql.OpContains != comparison.Op {
		t.Errorf("op = %v, want :", comparison.Op)
	}
}

// A quoted string separated by a space is free text, not a value.
func TestSpaceBreaksValueBinding(t *testing.T) {
	node := parse(t, `msg:foo "separate text"`)

	and, ok := node.(*rql.And)
	if !ok {
		t.Fatalf("parsed to %T, want And", node)
	}
	if 2 != len(and.Children) {
		t.Fatalf("children = %d, want 2", len(and.Children))
	}
	if _, ok := and.Children[1].(*rql.Text); !ok {
		t.Errorf("second child = %T, want Text", and.Children[1])
	}
}

func TestEmptyQueryMatchesEverything(t *testing.T) {
	for _, input := range []string{"", "   ", "\t\n"} {
		node, err := rql.Parse(input)
		if nil != err {
			t.Errorf("Parse(%q) errored: %v", input, err)
		}
		if nil != node {
			t.Errorf("Parse(%q) = %v, want nil", input, node)
		}
	}
}

/*!
 * A half-written query is the normal state of a search bar, so errors carry a
 * position and never panic.
 */
func TestParseErrors(t *testing.T) {
	cases := []string{
		`(a=1`,
		`a=1)`,
		`"unterminated`,
		`=42`,
		`()`,
		`a=1 AND`,
		`NOT`,
		`level>=`,
	}

	for _, input := range cases {
		node, err := rql.Parse(input)

		if nil == err {
			t.Errorf("Parse(%q) = %v, want an error", input, node)
			continue
		}

		var parseErr *rql.Error
		if !asError(err, &parseErr) {
			t.Errorf("Parse(%q) gave %T, want *rql.Error", input, err)
			continue
		}
		if parseErr.Position < 0 {
			t.Errorf("Parse(%q) reported position %d", input, parseErr.Position)
		}
		if "" == parseErr.Message {
			t.Errorf("Parse(%q) gave an empty message", input)
		}
	}
}

func asError(err error, target **rql.Error) bool {
	if e, ok := err.(*rql.Error); ok {
		*target = e
		return true
	}
	return false
}

// Partial input while typing must never panic.
func TestPrefixesNeverPanic(t *testing.T) {
	full := `level>=ERROR service="api" -path:/health (a=1 OR b:~"^x") "free text"`

	for i := range len(full) + 1 {
		func() {
			defer func() {
				if r := recover(); nil != r {
					t.Fatalf("panic on prefix %q: %v", full[:i], r)
				}
			}()
			rql.Parse(full[:i])
		}()
	}
}

func TestFieldsListsReferencedKeys(t *testing.T) {
	fields := rql.Fields(parse(t, `level=ERROR user_id>5 (a=1 OR level=WARN) "text"`))

	want := map[string]bool{"level": true, "user_id": true, "a": true}
	if len(want) != len(fields) {
		t.Fatalf("fields = %v, want %d distinct", fields, len(want))
	}
	for _, field := range fields {
		if !want[field] {
			t.Errorf("unexpected field %q", field)
		}
	}
}

func TestSeverityRanking(t *testing.T) {
	if rql.Rank("error") <= rql.Rank("warn") {
		t.Error("ERROR must outrank WARN")
	}
	if rql.Rank("FATAL") <= rql.Rank("ERROR") {
		t.Error("FATAL must outrank ERROR")
	}
	// An unknown level sits above DEBUG and below WARN, so `level>=ERROR` does
	// not fill up with everyone's bespoke levels.
	unknown := rql.Rank("AUDIT")
	if unknown <= rql.Rank("DEBUG") || unknown >= rql.Rank("WARN") {
		t.Errorf("unknown level ranked %d, want between DEBUG and WARN", unknown)
	}
}

// Values are bound, never interpolated. A search box is about as exposed as an
// input gets.
func TestSQLBindsRatherThanInterpolates(t *testing.T) {
	node := parse(t, `service="'; DROP TABLE files; --"`)

	compiled, err := rql.ToSQL(node, fakeResolver{})
	if nil != err {
		t.Fatal(err)
	}

	if strings.Contains(compiled.Expr, "DROP") {
		t.Errorf("value was interpolated into the SQL: %s", compiled.Expr)
	}
	if 1 != len(compiled.Args) {
		t.Fatalf("args = %v, want the value bound", compiled.Args)
	}
	if `'; DROP TABLE files; --` != compiled.Args[0] {
		t.Errorf("bound arg = %v", compiled.Args[0])
	}
}

// LIKE wildcards inside a user's substring must be escaped, or searching for
// "100%" matches everything starting with 100.
func TestLikeWildcardsAreEscaped(t *testing.T) {
	compiled, err := rql.ToSQL(parse(t, `msg:100%`), fakeResolver{})
	if nil != err {
		t.Fatal(err)
	}

	if `%100\%%` != compiled.Args[0] {
		t.Errorf("bound arg = %q, want the %% escaped", compiled.Args[0])
	}
}

// A field no file could have folds to FALSE rather than emitting a scan that
// cannot match.
func TestUnresolvableFieldFoldsToFalse(t *testing.T) {
	compiled, err := rql.ToSQL(parse(t, `nope=1`), fakeResolver{unresolvable: map[string]bool{"nope": true}})
	if nil != err {
		t.Fatal(err)
	}

	if "FALSE" != compiled.Expr {
		t.Errorf("expr = %q, want FALSE", compiled.Expr)
	}
}

/*!
 * NOT of an absent field must match.
 *
 * SQL's NOT NULL is NULL, so without the coalesce a row where the field is
 * missing would vanish from `-level=ERROR` — which is the opposite of what a
 * person means by "not".
 */
func TestNegationCoalescesNulls(t *testing.T) {
	compiled, err := rql.ToSQL(parse(t, `-level=ERROR`), fakeResolver{})
	if nil != err {
		t.Fatal(err)
	}

	if !strings.Contains(compiled.Expr, "coalesce") {
		t.Errorf("expr = %q, want a coalesce around the negated predicate", compiled.Expr)
	}
}

// fakeResolver resolves everything to a column of its own name.
type fakeResolver struct {
	unresolvable map[string]bool
	numeric      map[string]bool
}

func (f fakeResolver) Resolve(field string) (string, rql.FieldKind, bool) {
	if f.unresolvable[field] {
		return "", rql.FieldString, false
	}
	if "level" == field {
		return "level", rql.FieldSeverity, true
	}
	if f.numeric[field] {
		return "a_" + field, rql.FieldNumber, true
	}
	return "a_" + field, rql.FieldString, true
}

/*!
 * A `-` pressed against a phrase or a group negates it.
 *
 * The lexer ends a word at a quote or a parenthesis, so `-"foo bar"` used to
 * arrive as the text "-" beside the phrase — and String renders a negated
 * phrase exactly that way, so the round trip broke too.
 */
func TestDashNegatesAPhraseOrAGroup(t *testing.T) {
	for input, want := range map[string]string{
		`-"foo bar"`:      `-"foo bar"`,
		`-'foo bar'`:      `-"foo bar"`,
		`-(a=1 OR b=2)`:   `NOT (a=1 OR b=2)`,
		`x -"foo bar" y`:  `x -"foo bar" y`,
		`NOT "foo bar"`:   `-"foo bar"`,
		`-msg:"foo bar"`:  `-msg:"foo bar"`,
		`a - b`:           `a "-" b`,
		`-"foo" OR "bar"`: `-foo OR bar`,
	} {
		node := parse(t, input)
		if got := rql.String(node); got != want {
			t.Errorf("String(Parse(%q)) = %q, want %q", input, got, want)
		}
		if again := rql.String(parse(t, rql.String(node))); again != rql.String(node) {
			t.Errorf("%q does not survive a second round trip: %q", input, again)
		}
	}

	not, ok := parse(t, `-"foo bar"`).(*rql.Not)
	if !ok {
		t.Fatalf(`-"foo bar" parsed to %T, want Not`, parse(t, `-"foo bar"`))
	}
	if text, ok := not.Child.(*rql.Text); !ok || "foo bar" != text.Value {
		t.Errorf("negated child = %#v, want the phrase", not.Child)
	}
}

// The keywords are upper case only. `user not found` is three words somebody
// is looking for, not `user AND NOT found`.
func TestLowerCaseKeywordsAreWords(t *testing.T) {
	for _, input := range []string{`user not found`, `read or write`, `this and that`} {
		and, ok := parse(t, input).(*rql.And)
		if !ok {
			t.Errorf("%q parsed to %T, want three ANDed words", input, parse(t, input))
			continue
		}
		for _, child := range and.Children {
			if _, ok := child.(*rql.Text); !ok {
				t.Errorf("%q has a %T child, want only text", input, child)
			}
		}
	}

	if _, ok := parse(t, `a NOT b`).(*rql.And).Children[1].(*rql.Not); !ok {
		t.Error("upper-case NOT stopped negating")
	}
	if _, ok := parse(t, `a OR b`).(*rql.Or); !ok {
		t.Error("upper-case OR stopped being an operator")
	}

	// And a keyword written as a phrase stays a phrase when rendered.
	if got := rql.String(parse(t, `"OR"`)); `"OR"` != got {
		t.Errorf(`String(Parse("\"OR\"")) = %q, want it still quoted`, got)
	}
}

// An apostrophe inside a word is a letter. It opens a quote only where a
// value or a negated phrase starts.
func TestApostrophesInsideWordsAreLetters(t *testing.T) {
	for input, want := range map[string]string{
		`don't`:          "don't",
		`user's profile`: "user's",
	} {
		node := parse(t, input)
		if and, ok := node.(*rql.And); ok {
			node = and.Children[0]
		}
		if text, ok := node.(*rql.Text); !ok || want != text.Value {
			t.Errorf("%q parsed to %#v, want the word %q", input, node, want)
		}
	}

	comparison, ok := parse(t, `msg:'connection reset'`).(*rql.Comparison)
	if !ok || "connection reset" != comparison.Value.Str || !comparison.Value.Quoted {
		t.Errorf(`msg:'connection reset' parsed to %#v, want a quoted value`, parse(t, `msg:'connection reset'`))
	}

	if comparison, ok := parse(t, `name=o'brien`).(*rql.Comparison); !ok || "o'brien" != comparison.Value.Str {
		t.Errorf(`name=o'brien parsed to %#v, want the value o'brien`, parse(t, `name=o'brien`))
	}
}

// nan, inf and hex are words, not numbers: the predicate cannot match NaN and
// SQL compared the text, so the two disagreed about `name=nan`.
func TestNonFiniteBarewordsStayStrings(t *testing.T) {
	for _, input := range []string{`n=nan`, `n=NaN`, `n=inf`, `n=-Infinity`, `n=0x1p4`, `n=1e999`} {
		comparison, ok := parse(t, input).(*rql.Comparison)
		if !ok {
			t.Errorf("%q did not parse to a comparison", input)
			continue
		}
		if rql.KindString != comparison.Value.Kind {
			t.Errorf("%q kind = %v, want a string", input, comparison.Value.Kind)
		}
	}
}

// A bad pattern is the query's fault, and both compilers say so the same way.
func TestBadRegexIsAParseError(t *testing.T) {
	node := parse(t, `msg:~"("`)

	_, sqlErr := rql.ToSQL(node, fakeResolver{})
	_, predicateErr := rql.ToPredicate(node)

	for name, err := range map[string]error{"sql": sqlErr, "predicate": predicateErr} {
		var parseErr *rql.Error
		if !asError(err, &parseErr) {
			t.Errorf("%s: err = %v (%T), want *rql.Error", name, err, err)
			continue
		}
		if !strings.Contains(parseErr.Message, "regular expression") {
			t.Errorf("%s: message = %q", name, parseErr.Message)
		}
	}
}

// Equality on level compares the stored name, after the alias a person typed
// is turned into it; ordering compares ranks.
func TestLevelEqualityComparesCanonicalNames(t *testing.T) {
	compiled, err := rql.ToSQL(parse(t, `level=warning`), fakeResolver{})
	if nil != err {
		t.Fatal(err)
	}
	if "(level = ?)" != compiled.Expr || "WARN" != compiled.Args[0] {
		t.Errorf("level=warning compiled to %s %v, want level = WARN", compiled.Expr, compiled.Args)
	}

	ordered, err := rql.ToSQL(parse(t, `level>=warning`), fakeResolver{})
	if nil != err {
		t.Fatal(err)
	}
	if rql.Rank("WARN") != ordered.Args[0] {
		t.Errorf("level>=warning bound %v, want WARN's rank", ordered.Args)
	}
}

func TestTimestampLiterals(t *testing.T) {
	for _, text := range []string{
		"2024-01-02", "2024-01-02T15:04", "2024-01-02T15:04:05", "2024-01-02 15:04:05.5",
		"2024-01-02T15:04:05Z", "2024-01-02T17:04:05+02:00",
	} {
		if _, err := rql.Timestamp(rql.Literal{Str: text}); nil != err {
			t.Errorf("Timestamp(%q): %v", text, err)
		}
	}

	// An offset is applied, not dropped.
	at, _ := rql.Timestamp(rql.Literal{Str: "2024-01-02T17:04:05+02:00"})
	if want := "2024-01-02T15:04:05Z"; want != at.Format("2006-01-02T15:04:05Z07:00") {
		t.Errorf("offset time = %s, want %s", at, want)
	}

	if _, err := rql.ToSQL(parse(t, `ts>yesterday`), timestampResolver{}); nil == err {
		t.Error("ts>yesterday compiled")
	}
	if _, err := rql.ToPredicate(parse(t, `ts>yesterday`)); nil == err {
		t.Error("ts>yesterday compiled to a predicate")
	}
}

type timestampResolver struct{}

func (timestampResolver) Resolve(string) (string, rql.FieldKind, bool) {
	return "ts", rql.FieldTimestamp, true
}

// A phrase with a backslash in it renders as one the lexer reads back the
// same. Only its quotes used to be escaped, so a trailing backslash escaped
// the closing quote instead.
func TestPhrasesWithBackslashesRoundTrip(t *testing.T) {
	for _, input := range []string{
		`"a b\\"`, `"C:\\temp dir"`, `-"a\\b c"`, `msg:"a\\b c"`, `"x\\"`, `"say \"hi\" \\ bye"`,
	} {
		first := parse(t, input)
		rendered := rql.String(first)

		second, err := rql.Parse(rendered)
		if nil != err {
			t.Errorf("%s rendered as %s, which does not parse: %v", input, rendered, err)
			continue
		}
		if !reflect.DeepEqual(first, second) {
			t.Errorf("%s rendered as %s, which parses to something else", input, rendered)
		}
	}
}

// A bare number is a level on pino's scale, which is how ingest stored it; a
// quoted one, or one off the scale, is the text it says.
func TestNumericLevelsArePinosScale(t *testing.T) {
	for query, want := range map[string]any{
		`level=50`:   "ERROR",
		`level=40`:   "WARN",
		`level=10`:   "TRACE",
		`level=60`:   "FATAL",
		`level="50"`: "50",
		`level=45`:   "45",
		`level>=40`:  rql.Rank("WARN"),
		`level<50`:   rql.Rank("ERROR"),
	} {
		compiled, err := rql.ToSQL(parse(t, query), fakeResolver{})
		if nil != err {
			t.Fatal(err)
		}
		if want != compiled.Args[0] {
			t.Errorf("%s bound %v, want %v", query, compiled.Args[0], want)
		}
	}

	for query, matches := range map[string]map[string]bool{
		`level=50`:  {"ERROR": true, "50": false},
		`level>=40`: {"WARN": true, "ERROR": true, "INFO": false, "40": false},
	} {
		predicate, err := rql.ToPredicate(parse(t, query))
		if nil != err {
			t.Fatal(err)
		}
		for level, want := range matches {
			if got := predicate(&model.Record{Level: level}); want != got {
				t.Errorf("%s on level %s = %v, want %v", query, level, got, want)
			}
		}
	}
}

// Text written as a number reads the way DuckDB's try_cast reads it, so the
// tail answers as search does.
func TestTextReadsAsANumberTheWayDuckDBDoes(t *testing.T) {
	for _, tc := range []struct {
		value, query string
		want         bool
	}{
		// Go's hex floats are not numbers to DuckDB.
		{"0x1p4", `v=16`, false},
		{"0X1P4", `v>0`, false},
		// Past the largest double is infinity to DuckDB, not an error.
		{"1e400", `v>1e300`, true},
		{"-1e400", `v<-1e300`, true},
		{" 12 ", `v=12`, true},
	} {
		predicate, err := rql.ToPredicate(parse(t, tc.query))
		if nil != err {
			t.Fatal(err)
		}

		rec := &model.Record{Fields: map[string]model.Value{"v": model.Str(tc.value)}}
		if got := predicate(rec); tc.want != got {
			t.Errorf("%s on %q = %v, want %v", tc.query, tc.value, got, tc.want)
		}
	}
}

// ts as text is DuckDB's rendering of a UTC TIMESTAMPTZ: trailing zeros
// trimmed and the offset written, which is also a form Timestamp reads back.
func TestTimestampTextIsDuckDBs(t *testing.T) {
	rec := &model.Record{Ts: time.Date(2024, 1, 2, 3, 4, 5, 120_000_000, time.UTC)}

	for query, want := range map[string]bool{
		`ts:"2024-01-02 03:04:05.12+00"`: true,
		`ts:~"05[.]12[+]00$"`:            true,
		`ts:"05.120"`:                    false,
	} {
		predicate, err := rql.ToPredicate(parse(t, query))
		if nil != err {
			t.Fatal(err)
		}
		if got := predicate(rec); want != got {
			t.Errorf("%s = %v, want %v", query, got, want)
		}
	}

	at, err := rql.Timestamp(rql.Literal{Str: "2024-01-02 03:04:05.12+00"})
	if nil != err || !at.Equal(rec.Ts) {
		t.Errorf("Timestamp of DuckDB's own text = %s, %v", at, err)
	}
}
