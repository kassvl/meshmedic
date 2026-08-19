// Package promql extracts the metric names and label keys a query depends on,
// so a catalog entry can be checked against a live Prometheus before an
// incident rather than during one.
//
// It tokenizes rather than pattern-matches. A regex over PromQL breaks on the
// things that actually appear in this catalog: braces inside string literals
// (`response_code=~"5.."` is fine, but `pod=~"{{.workload}}-.*"` is not),
// nested function calls, durations, and comments. The tokenizer below knows
// about string literals, so a brace inside one is never mistaken for a
// selector.
//
// It deliberately does not depend on github.com/prometheus/prometheus. That
// module brings 253 modules and 259 packages into a build that currently has
// one dependency, which would contradict the property the tool sells: a
// minimal, auditable binary holding no cluster credentials. The cost is that
// this is a lexical extractor, not a full parser: it finds the names a query
// references, which is exactly what checking against a live Prometheus needs,
// and it does not validate that the query is semantically well-formed. That
// job already belongs to Prometheus itself, which rejects a malformed query
// the first time the detector runs it.
package promql

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// PromQL keywords that are never metric names.
var keywords = map[string]bool{
	"by": true, "without": true, "on": true, "ignoring": true,
	"group_left": true, "group_right": true, "offset": true, "bool": true,
	"and": true, "or": true, "unless": true, "start": true, "end": true,
	"atan2": true, "inf": true, "nan": true,
}

// groupingKeywords introduce a parenthesised list of label names.
var groupingKeywords = map[string]bool{
	"by": true, "without": true, "on": true, "ignoring": true,
	"group_left": true, "group_right": true,
}

// aggregators are never metric names even though they can be followed by a
// grouping clause rather than "(": `sum by (le) (rate(...))` would otherwise
// report a metric called "sum".
var aggregators = map[string]bool{
	"sum": true, "min": true, "max": true, "avg": true, "group": true,
	"stddev": true, "stdvar": true, "count": true, "count_values": true,
	"bottomk": true, "topk": true, "quantile": true, "limitk": true,
	"limit_ratio": true,
}

// Refs is what a query depends on.
type Refs struct {
	Metrics []string // metric names, sorted and deduplicated
	Labels  []string // label keys referenced by matchers or grouping
}

// Extract returns the metric names and label keys the query references.
func Extract(query string) (Refs, error) {
	toks, err := tokenize(query)
	if err != nil {
		return Refs{}, err
	}
	metrics := map[string]bool{}
	labels := map[string]bool{}

	depth := 0   // brace nesting: inside {} we are in a label matcher list
	bracket := 0 // bracket nesting: inside [] is a range or subquery step
	// consumed marks tokens already accounted for by a grouping clause, so a
	// label name in `by (le)` is not re-read as a metric on the next pass.
	consumed := map[int]bool{}
	for i, tk := range toks {
		if consumed[i] {
			continue
		}
		switch {
		case tk.kind == punct && tk.text == "[":
			bracket++
		case tk.kind == punct && tk.text == "]":
			if bracket > 0 {
				bracket--
			}
		case bracket > 0:
			// A range selector or subquery step: `[30m:1m]`. Nothing in here
			// is a metric or a label, and the step's leading colon would
			// otherwise lex as the start of an identifier.
		case tk.kind == punct && tk.text == "{":
			depth++
		case tk.kind == punct && tk.text == "}":
			if depth > 0 {
				depth--
			}
		case tk.kind == ident && depth > 0:
			// Inside a selector: an identifier followed by a matcher operator
			// is a label key. __name__ is special: its value is a metric.
			if next := peek(toks, i+1); next != nil && next.kind == punct && isMatcher(next.text) {
				if tk.text == "__name__" {
					if v := peek(toks, i+2); v != nil && v.kind == str {
						metrics[v.text] = true
					}
					continue
				}
				labels[tk.text] = true
			}
		case tk.kind == ident && depth == 0:
			if keywords[tk.text] {
				// A grouping clause names label keys, which are worth
				// checking too: `sum by (destination_workload)` silently
				// collapses to one series if that label is gone.
				if groupingKeywords[tk.text] {
					collectGrouping(toks, i+1, labels, consumed)
				}
				continue
			}
			if aggregators[tk.text] {
				continue
			}
			next := peek(toks, i+1)
			// An identifier followed by "(" is a function call, not a metric.
			if next != nil && next.kind == punct && next.text == "(" {
				continue
			}
			metrics[tk.text] = true
		}
	}
	return Refs{Metrics: sortedKeys(metrics), Labels: sortedKeys(labels)}, nil
}

// collectGrouping reads the parenthesised label list following a grouping
// keyword and records each name.
func collectGrouping(toks []token, start int, labels map[string]bool, consumed map[int]bool) {
	if start >= len(toks) || toks[start].kind != punct || toks[start].text != "(" {
		return
	}
	consumed[start] = true
	for i := start + 1; i < len(toks); i++ {
		switch {
		case toks[i].kind == ident:
			labels[toks[i].text] = true
			consumed[i] = true
		case toks[i].kind == punct && toks[i].text == ",":
			consumed[i] = true
		case toks[i].kind == punct && toks[i].text == ")":
			consumed[i] = true
			return
		default:
			return
		}
	}
}

func isMatcher(s string) bool {
	switch s {
	case "=", "!=", "=~", "!~":
		return true
	}
	return false
}

func peek(toks []token, i int) *token {
	if i < 0 || i >= len(toks) {
		return nil
	}
	return &toks[i]
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type kind int

const (
	ident kind = iota
	str
	num
	punct
)

type token struct {
	kind kind
	text string
}

// tokenize splits PromQL into the tokens the extractor needs. Durations and
// numbers are lexed only so they cannot be mistaken for identifiers.
func tokenize(s string) ([]token, error) {
	var toks []token
	rs := []rune(s)
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '#':
			// Comment to end of line.
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'' || c == '`':
			text, next, err := lexString(rs, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{str, text})
			i = next
		case isIdentStart(c):
			j := i
			for j < len(rs) && isIdentPart(rs[j]) {
				j++
			}
			toks = append(toks, token{ident, string(rs[i:j])})
			i = j
		case unicode.IsDigit(c):
			j := i
			// A number, possibly a duration like 2m or 1h30m.
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.' || isDurationUnit(rs[j])) {
				j++
			}
			toks = append(toks, token{num, string(rs[i:j])})
			i = j
		default:
			// Two-rune operators first, so =~ is not read as = then ~.
			if i+1 < len(rs) {
				two := string(rs[i : i+2])
				switch two {
				case "=~", "!~", "!=", "==", ">=", "<=", "=="[:2]:
					toks = append(toks, token{punct, two})
					i += 2
					continue
				}
			}
			toks = append(toks, token{punct, string(c)})
			i++
		}
	}
	return toks, nil
}

// lexString reads a quoted string, honouring backslash escapes in the two
// quote styles that support them. It returns the unquoted contents.
func lexString(rs []rune, start int) (string, int, error) {
	quote := rs[start]
	var b strings.Builder
	for i := start + 1; i < len(rs); i++ {
		c := rs[i]
		if c == '\\' && quote != '`' && i+1 < len(rs) {
			// Keep the escape's target verbatim; the extractor only cares
			// about the presence of a string, not its exact value, except
			// for __name__ where escapes are vanishingly unlikely.
			b.WriteRune(rs[i+1])
			i++
			continue
		}
		if c == quote {
			return b.String(), i + 1, nil
		}
		b.WriteRune(c)
	}
	return "", 0, fmt.Errorf("unterminated string starting at offset %d", start)
}

func isIdentStart(c rune) bool {
	return unicode.IsLetter(c) || c == '_' || c == ':'
}

func isIdentPart(c rune) bool {
	return unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' || c == ':'
}

func isDurationUnit(c rune) bool {
	switch c {
	case 's', 'm', 'h', 'd', 'w', 'y':
		return true
	}
	return false
}
