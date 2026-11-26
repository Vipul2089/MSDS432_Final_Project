package router

import (
	"encoding/json"
	"strings"
	"testing"
)

// Each input is checked against both tiers, so a change to one side that
// the other does not follow shows up here.
func TestParseText(t *testing.T) {
	tests := []struct {
		in       string
		wantCold []string // one regex per clause; "!" marks a negated clause
		wantHot  string
	}{
		{"Neptune", []string{`\bneptune\b`},
			`{"must":[{"match_phrase":{"raw_message":"neptune"}}]}`},
		{"tcp -neptune", []string{`\btcp\b`, `!\bneptune\b`},
			`{"must":[{"match_phrase":{"raw_message":"tcp"}}],"must_not":[{"match_phrase":{"raw_message":"neptune"}}]}`},
		{"smurf | neptune icmp", []string{`(?:\bsmurf\b|\bneptune\b)`, `\bicmp\b`},
			`{"must":[{"bool":{"minimum_should_match":1,"should":[{"match_phrase":{"raw_message":"smurf"}},{"match_phrase":{"raw_message":"neptune"}}]}},{"match_phrase":{"raw_message":"icmp"}}]}`},
		{`"private REJ" -"eco_i"`, []string{`\bprivate\W+rej\b`, `!\beco_i\b`},
			`{"must":[{"match_phrase":{"raw_message":"private rej"}}],"must_not":[{"match_phrase":{"raw_message":"eco_i"}}]}`},
		{"port* a|b", []string{`\bport`, `(?:\ba\b|\bb\b)`},
			`{"must":[{"prefix":{"raw_message":"port"}},{"bool":{"minimum_should_match":1,"should":[{"match_phrase":{"raw_message":"a"}},{"match_phrase":{"raw_message":"b"}}]}}]}`},
		{"0.00 10.67.143.177", []string{`\b0\W+00\b`, `\b10\W+67\W+143\W+177\b`},
			`{"must":[{"match_phrase":{"raw_message":"0 00"}},{"match_phrase":{"raw_message":"10 67 143 177"}}]}`},
	}
	for _, tt := range tests {
		q, err := parseText(tt.in)
		if err != nil {
			t.Errorf("%q: %v", tt.in, err)
			continue
		}
		var cold []string
		for _, c := range q {
			p := c.pattern()
			if c.negate {
				p = "!" + p
			}
			cold = append(cold, p)
		}
		if strings.Join(cold, " ; ") != strings.Join(tt.wantCold, " ; ") {
			t.Errorf("%q: cold = %q, want %q", tt.in, cold, tt.wantCold)
		}
		must, mustNot := q.hot()
		parts := map[string]any{"must": must}
		if mustNot != nil {
			parts["must_not"] = mustNot
		}
		if got, _ := json.Marshal(parts); string(got) != tt.wantHot {
			t.Errorf("%q: hot = %s\nwant %s", tt.in, got, tt.wantHot)
		}
	}
}

func TestParseTextErrors(t *testing.T) {
	for _, in := range []string{
		`"unclosed`, `""`, "a |", "| a", "a | | b", "-", "a -|b", "-a | b", "a | -b",
		"(a)", "a~2", "+a", "a*b", "-*", ".*", `a\b`, "...", `"- -"`,
	} {
		if _, err := parseText(in); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
	if q, err := parseText("   "); err != nil || len(q) != 0 {
		t.Errorf("blank text: got %v, %v", q, err)
	}
}
