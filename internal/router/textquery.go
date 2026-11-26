package router

import (
	"errors"
	"regexp"
	"strings"
)

// The search text is parsed once and then turned into an OpenSearch query
// for HOT and WARM and into SQL for COLD, so every tier reads it the same
// way (ADR-0013). Supported syntax:
//
//	a b      both words must appear
//	a | b    either word; binds tighter than the space, so "a | b c" is (a or b) and c
//	-a       a must not appear
//	"a b"    the words appear next to each other
//	ab*      a word starting with ab
//
// A word is a run of ASCII letters, digits and _, lowercased. HOT indexes
// raw_message with the same rule (the raw_words analyzer in the index
// template), so "0.00" is the words 0 and 00 next to each other on both
// tiers. Anything else that looks like query syntax is rejected rather than
// silently read in different ways.

// term is a sequence of adjacent words, or a single-word prefix.
type term struct {
	words  []string
	prefix bool
}

// clause matches if any of its terms matches, or if none does when negated.
type clause struct {
	any    []term
	negate bool
}

// textQuery matches if every clause matches.
type textQuery []clause

var (
	wordRun    = regexp.MustCompile(`[a-z0-9_]+`)
	prefixWord = regexp.MustCompile(`^[a-z0-9_]+$`)
)

func parseText(s string) (textQuery, error) {
	var (
		q         textQuery
		orPending bool
	)
	add := func(t term, negate bool) error {
		if !orPending {
			q = append(q, clause{any: []term{t}, negate: negate})
			return nil
		}
		if negate || q[len(q)-1].negate {
			return errors.New("a negated term (-a) cannot be part of an | group")
		}
		q[len(q)-1].any = append(q[len(q)-1].any, t)
		orPending = false
		return nil
	}

	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ' ' || c == '\t':
			i++
			continue
		case c == '|':
			if len(q) == 0 || orPending {
				return nil, errors.New("| must be placed between two terms")
			}
			orPending = true
			i++
			continue
		}

		negate := s[i] == '-'
		if negate {
			i++
			if i == len(s) || s[i] == ' ' || s[i] == '|' {
				return nil, errors.New("- must be followed by a word or a phrase")
			}
		}

		var t term
		if s[i] == '"' {
			end := strings.IndexByte(s[i+1:], '"')
			if end < 0 {
				return nil, errors.New(`a phrase is missing its closing "`)
			}
			raw := s[i+1 : i+1+end]
			if strings.ContainsAny(raw, `()~+*\`) {
				return nil, errors.New(`( ) ~ + * and \ are not supported, except * at the end of a word`)
			}
			t.words = wordRun.FindAllString(strings.ToLower(raw), -1)
			if len(t.words) == 0 {
				return nil, errors.New("a phrase must contain a letter or digit")
			}
			i += end + 2
		} else {
			end := i
			for end < len(s) && !strings.ContainsRune(" \t|\"", rune(s[end])) {
				end++
			}
			w := strings.ToLower(s[i:end])
			i = end
			if strings.HasSuffix(w, "*") {
				w, t.prefix = w[:len(w)-1], true
				if !prefixWord.MatchString(w) {
					return nil, errors.New("a prefix (ab*) may contain only letters, digits and _")
				}
			}
			if strings.ContainsAny(w, `()~+*\`) {
				return nil, errors.New(`( ) ~ + * and \ are not supported, except * at the end of a word`)
			}
			t.words = wordRun.FindAllString(w, -1)
			if len(t.words) == 0 {
				return nil, errors.New("a term must contain a letter or digit")
			}
		}
		if err := add(t, negate); err != nil {
			return nil, err
		}
	}
	if orPending {
		return nil, errors.New("| must be placed between two terms")
	}
	return q, nil
}

// hot returns the clauses as OpenSearch bool query parts. A phrase query on
// one word is an ordinary word match.
func (q textQuery) hot() (must, mustNot []any) {
	for _, c := range q {
		alts := make([]any, len(c.any))
		for i, t := range c.any {
			if t.prefix {
				alts[i] = map[string]any{"prefix": map[string]any{"raw_message": t.words[0]}}
			} else {
				alts[i] = map[string]any{"match_phrase": map[string]any{"raw_message": strings.Join(t.words, " ")}}
			}
		}
		one := alts[0]
		if len(alts) > 1 {
			one = map[string]any{"bool": map[string]any{"should": alts, "minimum_should_match": 1}}
		}
		if c.negate {
			mustNot = append(mustNot, one)
		} else {
			must = append(must, one)
		}
	}
	return must, mustNot
}

// pattern returns the regular expression COLD uses for the clause, to be
// matched against the lowercased raw message. Adjacent words may be
// separated by any non-word characters, as on HOT. Words contain only
// [a-z0-9_], so they need no escaping.
func (c clause) pattern() string {
	alts := make([]string, len(c.any))
	for i, t := range c.any {
		alts[i] = `\b` + strings.Join(t.words, `\W+`)
		if !t.prefix {
			alts[i] += `\b`
		}
	}
	if len(alts) == 1 {
		return alts[0]
	}
	return "(?:" + strings.Join(alts, "|") + ")"
}
