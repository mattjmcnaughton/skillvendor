// Package review builds the self-contained prompt that `skillvendor prompt`
// emits and locates the model's JSON response for `skillvendor verdict`. It
// performs no network access and executes nothing: inputs are a skill
// directory on disk and bytes on stdin.
package review

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// SchemaID is the `$id` of the embedded response schema.
const SchemaID = "skillvendor/review/v1"

//go:embed schema.json
var schema []byte

// Schema returns the JSON schema a reviewer's response must match.
func Schema() []byte { return schema }

// Risk levels, in ascending order of severity.
var riskLevels = []string{"none", "low", "medium", "high", "critical"}

// RiskLevel returns the ordinal of a risk value (none=0 .. critical=4) and
// whether it is one of the five enum values.
func RiskLevel(risk string) (int, bool) {
	for i, r := range riskLevels {
		if r == risk {
			return i, true
		}
	}
	return 0, false
}

// Response is the decoded reviewer verdict. Only Risk is interpreted;
// Summary and Findings are display-only, so findings keep whatever shape the
// model produced rather than being validated against the schema.
type Response struct {
	Risk     string            `json:"risk"`
	Summary  string            `json:"summary"`
	Findings []json.RawMessage `json:"findings"`
}

// FormatFinding renders one finding on a single line for stderr. An object's
// known keys are laid out as "[severity] category file:line: description"
// and any other keys are appended as compact JSON; a finding of any other
// shape is printed as-is, so nothing the model said is lost.
func FormatFinding(raw json.RawMessage) string {
	var f map[string]interface{}
	if err := json.Unmarshal(raw, &f); err != nil {
		return oneLine(string(bytes.TrimSpace(raw)))
	}
	var b strings.Builder
	str := func(k string) string {
		v, ok := f[k]
		if !ok || v == nil {
			return ""
		}
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	if s := str("severity"); s != "" {
		fmt.Fprintf(&b, "[%s] ", s)
	}
	if s := str("category"); s != "" {
		b.WriteString(s + " ")
	}
	if s := str("file"); s != "" {
		b.WriteString(s)
		if l := str("line"); l != "" {
			b.WriteString(":" + l)
		}
		b.WriteString(": ")
	}
	b.WriteString(str("description"))
	known := map[string]bool{"severity": true, "category": true, "file": true, "line": true, "description": true}
	var extra []string
	for k := range f {
		if !known[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		rest := make(map[string]interface{}, len(extra))
		for _, k := range extra {
			rest[k] = f[k]
		}
		if enc, err := json.Marshal(rest); err == nil {
			b.WriteString(" " + string(enc))
		}
	}
	return oneLine(b.String())
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ErrNotFound is returned by Locate when the input contains no JSON object
// with a valid `risk` value.
var ErrNotFound = errors.New("no review response with a valid risk found")

// Locate extracts the reviewer's response from raw model output. In order:
//
//  1. Input is a JSON object with a `structured_output` field: use it
//     (Claude Code `--output-format json --json-schema`).
//  2. Input is a JSON object with a `result` string: search that string.
//  3. Input is a JSON object matching the schema directly.
//  4. Otherwise the last well-formed JSON object in the text with a valid
//     `risk`, whether fenced in markdown or bare.
//
// A response is only accepted when `risk` is one of the five enum values.
func Locate(input []byte) (*Response, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(input), &envelope); err == nil {
		if raw, ok := envelope["structured_output"]; ok {
			if r, err := decode(raw); err == nil {
				return r, nil
			}
		}
		if raw, ok := envelope["result"]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				if r, err := lastObject(s); err == nil {
					return r, nil
				}
			}
		}
		if r, err := decode(input); err == nil {
			return r, nil
		}
	}
	return lastObject(string(input))
}

// decode parses exactly one JSON object and validates its risk.
func decode(raw []byte) (*Response, error) {
	var r Response
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if _, ok := RiskLevel(r.Risk); !ok {
		return nil, fmt.Errorf("invalid risk %q", r.Risk)
	}
	return &r, nil
}

// lastObject scans text for the last `{` that begins a well-formed JSON
// object with a valid risk. Scanning from the end means a nested object (a
// finding) is tried first and rejected for lacking `risk`, so the enclosing
// response wins.
func lastObject(text string) (*Response, error) {
	for i := strings.LastIndex(text, "{"); i >= 0; i = strings.LastIndex(text[:i], "{") {
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		var r Response
		if err := dec.Decode(&r); err != nil {
			continue
		}
		if _, ok := RiskLevel(r.Risk); ok {
			return &r, nil
		}
	}
	return nil, ErrNotFound
}
