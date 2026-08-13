/*!
 * Lexing and parsing rQL.
 *
 * The grammar, informally:
 *
 *     query      := or
 *     or         := and (("OR" | "or") and)*
 *     and        := unary (("AND" | "and")? unary)*      // AND is implicit
 *     unary      := ("-" | "NOT") unary | primary
 *     primary    := "(" or ")" | comparison | text
 *     comparison := field op value
 *     op         := "=" | ":" | ":~" | "<" | "<=" | ">" | ">="
 *
 * The awkward part of a search bar is that people do not put spaces around
 * operators, and values contain characters that look like operators:
 * `level>=ERROR`, `-path:/health`, `region=us-east-1`. So the lexer does not
 * try to tokenize operators globally. It reads whitespace-delimited *chunks*
 * and the parser splits each chunk at its first operator — which makes
 * `us-east-1` a value rather than a subtraction, without any lookahead
 * cleverness.
 *
 * The other rule that matters: a half-written query is the normal state of a
 * search bar. Parse returns an error with a position rather than panicking or
 * silently matching everything, and the UI shows it inline while the user is
 * still typing.
 */

package rql

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Error is a parse failure with the offset it happened at.
type Error struct {
	Message  string
	Position int
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s at position %d", e.Message, e.Position)
}

type tokenKind uint8

const (
	tokenWord tokenKind = iota
	tokenString
	tokenLParen
	tokenRParen
	tokenEOF
)

type token struct {
	kind tokenKind
	text string
	pos  int
	// adjacent records that no whitespace preceded this token, which is what
	// lets `msg:"has spaces"` bind the quoted string to the operator before it.
	adjacent bool
}

/*!
 * lex splits the input into chunks, parens and quoted strings.
 */
func lex(input string) ([]token, error) {
	var tokens []token

	runes := []rune(input)
	i := 0
	space := true

	for i < len(runes) {
		if unicode.IsSpace(runes[i]) {
			space = true
			i++
			continue
		}

		start := i

		switch runes[i] {
		case '(':
			tokens = append(tokens, token{kind: tokenLParen, pos: start, adjacent: !space})
			i++

		case ')':
			tokens = append(tokens, token{kind: tokenRParen, pos: start, adjacent: !space})
			i++

		case '"', '\'':
			text, next, err := lexQuoted(runes, i)
			if nil != err {
				return nil, err
			}
			tokens = append(tokens, token{kind: tokenString, text: text, pos: start, adjacent: !space})
			i = next

		default:
			for i < len(runes) &&
				!unicode.IsSpace(runes[i]) &&
				'(' != runes[i] && ')' != runes[i] &&
				'"' != runes[i] && '\'' != runes[i] {
				i++
			}
			tokens = append(tokens, token{kind: tokenWord, text: string(runes[start:i]), pos: start, adjacent: !space})
		}

		space = false
	}

	return append(tokens, token{kind: tokenEOF, pos: len(runes)}), nil
}

// lexQuoted reads a quoted string, honouring backslash escapes.
func lexQuoted(runes []rune, start int) (string, int, error) {
	quote := runes[start]

	var sb strings.Builder
	i := start + 1

	for i < len(runes) {
		switch runes[i] {
		case '\\':
			if i+1 < len(runes) {
				sb.WriteRune(runes[i+1])
				i += 2
				continue
			}
			i++

		case quote:
			return sb.String(), i + 1, nil

		default:
			sb.WriteRune(runes[i])
			i++
		}
	}

	return "", 0, &Error{Message: "unterminated quoted string", Position: start}
}

// operators, longest first so `>=` is not read as `>`.
var operators = []struct {
	text string
	op   Op
}{
	{":~", OpMatches},
	{">=", OpGreaterEqual},
	{"<=", OpLessEqual},
	{"=", OpEquals},
	{":", OpContains},
	{">", OpGreater},
	{"<", OpLess},
}

type parser struct {
	tokens []token
	pos    int
}

/*!
 * Parse turns a query string into an AST.
 *
 * An empty or whitespace-only query is not an error — it is a query that
 * matches everything, which is what an empty search bar should do — and
 * returns a nil Node that both compilers understand.
 */
func Parse(input string) (Node, error) {
	tokens, err := lex(input)
	if nil != err {
		return nil, err
	}

	p := &parser{tokens: tokens}

	if tokenEOF == p.peek().kind {
		return nil, nil
	}

	node, err := p.parseOr()
	if nil != err {
		return nil, err
	}

	if tokenEOF != p.peek().kind {
		return nil, &Error{Message: "unexpected " + describe(p.peek()), Position: p.peek().pos}
	}

	return node, nil
}

func describe(t token) string {
	switch t.kind {
	case tokenRParen:
		return "closing parenthesis"
	case tokenLParen:
		return "opening parenthesis"
	case tokenEOF:
		return "end of query"
	default:
		return strconv.Quote(t.text)
	}
}

func (p *parser) peek() token { return p.tokens[p.pos] }
func (p *parser) next() token { t := p.tokens[p.pos]; p.pos++; return t }
func (p *parser) rewind()     { p.pos-- }
func (p *parser) atEnd() bool { return tokenEOF == p.peek().kind }

// keyword reports whether an unquoted token is the given bare keyword.
func keyword(t token, word string) bool {
	return tokenWord == t.kind && strings.EqualFold(t.text, word)
}

func (p *parser) parseOr() (Node, error) {
	left, err := p.parseAnd()
	if nil != err {
		return nil, err
	}

	var children []Node

	for keyword(p.peek(), "OR") {
		p.next()

		right, err := p.parseAnd()
		if nil != err {
			return nil, err
		}

		if 0 == len(children) {
			children = append(children, left)
		}
		children = append(children, right)
	}

	if 0 == len(children) {
		return left, nil
	}

	return &Or{Children: children}, nil
}

func (p *parser) parseAnd() (Node, error) {
	first, err := p.parseUnary()
	if nil != err {
		return nil, err
	}

	children := []Node{first}

	for {
		if p.atEnd() || tokenRParen == p.peek().kind || keyword(p.peek(), "OR") {
			break
		}

		// AND is optional; two terms side by side are anded.
		if keyword(p.peek(), "AND") {
			p.next()

			if p.atEnd() || tokenRParen == p.peek().kind {
				return nil, &Error{Message: "AND with nothing after it", Position: p.peek().pos}
			}
		}

		next, err := p.parseUnary()
		if nil != err {
			return nil, err
		}

		children = append(children, next)
	}

	if 1 == len(children) {
		return first, nil
	}

	return &And{Children: children}, nil
}

func (p *parser) parseUnary() (Node, error) {
	if keyword(p.peek(), "NOT") {
		at := p.next()

		if p.atEnd() {
			return nil, &Error{Message: "NOT with nothing after it", Position: at.pos}
		}

		child, err := p.parseUnary()
		if nil != err {
			return nil, err
		}

		return &Not{Child: child}, nil
	}

	// A leading `-` on a chunk negates it: `-path:/health`. The rest of the
	// chunk is re-analysed, so `-` is never confused with a minus sign inside
	// a value — those only ever appear after an operator.
	if t := p.peek(); tokenWord == t.kind && strings.HasPrefix(t.text, "-") && len(t.text) > 1 {
		p.next()
		p.tokens[p.pos-1].text = strings.TrimPrefix(t.text, "-")
		p.rewind()

		child, err := p.parsePrimary()
		if nil != err {
			return nil, err
		}

		return &Not{Child: child}, nil
	}

	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Node, error) {
	t := p.next()

	switch t.kind {
	case tokenLParen:
		if tokenRParen == p.peek().kind {
			return nil, &Error{Message: "empty parentheses", Position: t.pos}
		}

		inner, err := p.parseOr()
		if nil != err {
			return nil, err
		}

		if tokenRParen != p.peek().kind {
			return nil, &Error{Message: "missing closing parenthesis", Position: p.peek().pos}
		}
		p.next()

		return inner, nil

	case tokenRParen:
		return nil, &Error{Message: "unexpected closing parenthesis", Position: t.pos}

	case tokenString:
		// A quoted string on its own is full-text.
		return &Text{Value: t.text}, nil

	case tokenEOF:
		return nil, &Error{Message: "unexpected end of query", Position: t.pos}
	}

	return p.parseChunk(t)
}

/*!
 * parseChunk splits a word at its first operator.
 *
 * This is where `level>=ERROR` becomes a comparison and `us-east-1` stays a
 * value: the split happens once, at the leftmost operator, so everything to
 * its right is value text no matter what characters it contains.
 */
func (p *parser) parseChunk(t token) (Node, error) {
	field, op, rest, found := splitOperator(t.text)

	if !found {
		return &Text{Value: t.text}, nil
	}

	if "" == field {
		return nil, &Error{Message: "comparison with no field name", Position: t.pos}
	}

	// `msg:"has spaces"` — the operator ends the chunk and the value is the
	// quoted token immediately after it.
	if "" == rest {
		if next := p.peek(); tokenString == next.kind && next.adjacent {
			p.next()
			return &Comparison{Field: field, Op: op, Value: Literal{Kind: KindString, Str: next.text, Quoted: true}}, nil
		}

		return nil, &Error{
			Message:  fmt.Sprintf("%q has no value after %q", field, op.String()),
			Position: t.pos,
		}
	}

	if "*" == rest {
		if OpEquals != op {
			return nil, &Error{Message: "* is only meaningful with =", Position: t.pos}
		}
		return &Comparison{Field: field, Op: OpExists, Value: Literal{Kind: KindWildcard}}, nil
	}

	return &Comparison{Field: field, Op: op, Value: literalOf(rest)}, nil
}

// splitOperator finds the leftmost operator in a chunk.
func splitOperator(chunk string) (field string, op Op, rest string, found bool) {
	best := -1
	var bestOp Op
	var bestLen int

	for _, candidate := range operators {
		at := strings.Index(chunk, candidate.text)
		if at < 0 {
			continue
		}

		// Leftmost wins; on a tie the longer operator wins, so `>=` beats `>`
		// and `:~` beats `:`.
		if best < 0 || at < best || (at == best && len(candidate.text) > bestLen) {
			best, bestOp, bestLen = at, candidate.op, len(candidate.text)
		}
	}

	if best < 0 {
		return "", 0, "", false
	}

	return chunk[:best], bestOp, chunk[best+bestLen:], true
}

/*!
 * literalOf coerces an unquoted value.
 *
 * Quoting suppresses all of this — `status="200"` is the string, `status=200`
 * is the number — which is the escape hatch for a field whose values happen to
 * look like something else.
 */
func literalOf(text string) Literal {
	switch strings.ToLower(text) {
	case "true":
		return Literal{Kind: KindBool, Bool: true, Str: text}
	case "false":
		return Literal{Kind: KindBool, Bool: false, Str: text}
	case "null", "nil":
		return Literal{Kind: KindNull, Str: text}
	}

	if n, err := strconv.ParseFloat(text, 64); nil == err {
		return Literal{Kind: KindNumber, Num: n, Str: text}
	}

	return Literal{Kind: KindString, Str: text}
}
