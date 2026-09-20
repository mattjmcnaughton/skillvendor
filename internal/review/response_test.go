package review

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSchemaIsValidJSONWithID(t *testing.T) {
	var s map[string]interface{}
	if err := json.Unmarshal(Schema(), &s); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if s["$id"] != SchemaID {
		t.Errorf("schema $id = %v, want %s", s["$id"], SchemaID)
	}
}

func TestRiskLevelOrdering(t *testing.T) {
	prev := -1
	for _, r := range []string{"none", "low", "medium", "high", "critical"} {
		lvl, ok := RiskLevel(r)
		if !ok || lvl <= prev {
			t.Errorf("RiskLevel(%q) = %d, %v; want increasing", r, lvl, ok)
		}
		prev = lvl
	}
	if _, ok := RiskLevel("severe"); ok {
		t.Error("RiskLevel accepted an unknown value")
	}
}

func TestLocate(t *testing.T) {
	direct := `{"risk":"low","summary":"fine","findings":[]}`
	cases := []struct {
		name    string
		in      string
		want    string // expected risk; "" means expect ErrNotFound-style failure
		summary string
	}{
		{"claude structured_output", `{"type":"result","result":"ignored","structured_output":` + direct + `}`, "low", "fine"},
		{"claude result string with fence", `{"type":"result","result":"Here you go:\n` + "```json\\n" + `{\"risk\":\"high\",\"summary\":\"bad\",\"findings\":[]}\\n` + "```" + `"}`, "high", "bad"},
		{"direct object", direct, "low", "fine"},
		{"bare text with trailing prose", "Analysis...\n" + `{"risk":"medium","summary":"hmm","findings":[{"severity":"medium","category":"network","file":"x","description":"curl"}]}` + "\nDone.", "medium", "hmm"},
		{"fenced in markdown", "```json\n" + direct + "\n```\n", "low", "fine"},
		{"last object wins", `{"risk":"none","summary":"a","findings":[]} then {"risk":"critical","summary":"b","findings":[]}`, "critical", "b"},
		{"nested finding objects do not shadow the response", `{"risk":"high","summary":"s","findings":[{"severity":"high","category":"other","file":"f","description":"d"},{"a":{"b":1}}]}`, "high", "s"},
		{"findings of odd shape are tolerated", `{"risk":"low","summary":"s","findings":["just a string", 3, null]}`, "low", "s"},
		{"structured_output with invalid risk falls through to nothing", `{"structured_output":{"risk":"severe","summary":"","findings":[]},"result":"no json here"}`, "", ""},
		{"invalid risk", `{"risk":"severe","summary":"","findings":[]}`, "", ""},
		{"risk of wrong type", `{"risk":3,"summary":"","findings":[]}`, "", ""},
		{"empty", "", "", ""},
		{"prose only", "I refuse to answer.", "", ""},
		{"truncated object", `{"risk":"high","summary":"cut off`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Locate([]byte(tc.in))
			if tc.want == "" {
				if err == nil {
					t.Fatalf("expected error, got %+v", r)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r.Risk != tc.want || r.Summary != tc.summary {
				t.Errorf("got risk=%q summary=%q, want %q/%q", r.Risk, r.Summary, tc.want, tc.summary)
			}
		})
	}
	if _, err := Locate([]byte("nothing")); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestFormatFinding(t *testing.T) {
	cases := map[string]string{
		`{"severity":"high","category":"exfiltration","file":"SKILL.md","line":12,"description":"reads ~/.ssh"}`: "[high] exfiltration SKILL.md:12: reads ~/.ssh",
		`{"severity":"info","category":"other","file":"a.sh","description":"multi\nline"}`:                       "[info] other a.sh: multi line",
		`{"description":"no file","extra":true}`:                                                                 `no file {"extra":true}`,
		`"a bare string"`:                                                                                        `"a bare string"`,
		`42`:                                                                                                     "42",
	}
	for in, want := range cases {
		if got := FormatFinding(json.RawMessage(in)); got != want {
			t.Errorf("FormatFinding(%s) = %q, want %q", in, got, want)
		}
	}
	if got := FormatFinding(json.RawMessage(`{"file":"x","description":"d"}`)); strings.Contains(got, "[") {
		t.Errorf("unexpected severity bracket in %q", got)
	}
}
