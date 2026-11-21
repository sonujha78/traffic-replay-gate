// Package rules loads the YAML file that says what counts as a regression and what is noise.
package rules

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Severity string

const (
	Fail   Severity = "fail"   // blocks the pull request
	Warn   Severity = "warn"   // shown in the report, does not block
	Ignore Severity = "ignore" // hidden
)

// Kinds of difference. The policy section of the rules file is keyed by these names.
const (
	KindStatus       = "status_change"
	KindHeader       = "header_change"
	KindRemoved      = "removed_field"
	KindAdded        = "added_field"
	KindType         = "type_change"
	KindValue        = "value_change"
	KindRequestError = "request_error"
)

var knownKinds = map[string]bool{
	KindStatus: true, KindHeader: true, KindRemoved: true, KindAdded: true,
	KindType: true, KindValue: true, KindRequestError: true,
}

type Endpoint struct {
	Match           string   `yaml:"match"` // for example "GET /orders/*"
	IgnoreFields    []string `yaml:"ignore_fields"`
	UnorderedArrays []string `yaml:"unordered_arrays"`
}

type Latency struct {
	MaxP95IncreasePct float64  `yaml:"max_p95_increase_pct"`
	MinIncreaseMS     float64  `yaml:"min_increase_ms"`
	Severity          Severity `yaml:"severity"`
}

type Rules struct {
	CompareHeaders  []string            `yaml:"compare_headers"`
	IgnoreFields    []string            `yaml:"ignore_fields"`
	UnorderedArrays []string            `yaml:"unordered_arrays"`
	Policy          map[string]Severity `yaml:"policy"`
	Latency         Latency             `yaml:"latency"`
	Endpoints       []Endpoint          `yaml:"endpoints"`
}

// Default is used for everything a rules file does not mention.
func Default() *Rules {
	return &Rules{
		CompareHeaders: []string{"Content-Type"},
		Policy: map[string]Severity{
			KindStatus: Fail, KindRemoved: Fail, KindType: Fail, KindValue: Fail,
			KindAdded: Warn, KindHeader: Warn, KindRequestError: Fail,
		},
		Latency: Latency{MaxP95IncreasePct: 20, MinIncreaseMS: 30, Severity: Fail},
	}
}

// Load reads a rules file on top of the defaults. Unknown keys are an error so typos are caught.
func Load(path string) (*Rules, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := Default()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(r); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := r.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

func validSeverity(s Severity) bool { return s == Fail || s == Warn || s == Ignore }

func (r *Rules) validate() error {
	for kind, sev := range r.Policy {
		if !knownKinds[kind] {
			return fmt.Errorf("unknown policy kind %q", kind)
		}
		if !validSeverity(sev) {
			return fmt.Errorf("policy %s: severity must be fail, warn or ignore, got %q", kind, sev)
		}
	}
	if !validSeverity(r.Latency.Severity) {
		return fmt.Errorf("latency severity must be fail, warn or ignore, got %q", r.Latency.Severity)
	}
	for _, e := range r.Endpoints {
		if _, _, ok := strings.Cut(strings.TrimSpace(e.Match), " "); !ok {
			return fmt.Errorf("endpoint match %q must look like \"GET /path\"", e.Match)
		}
	}
	return nil
}

// SeverityOf returns how serious a kind of difference is. Unknown kinds fail, to be safe.
func (r *Rules) SeverityOf(kind string) Severity {
	if s, ok := r.Policy[kind]; ok {
		return s
	}
	return Fail
}

// MatchPattern reports whether a request matches "METHOD /path". A "*" segment matches exactly one segment.
func MatchPattern(pattern, method, path string) bool {
	pm, pp, ok := strings.Cut(strings.TrimSpace(pattern), " ")
	if !ok {
		return false
	}
	if pm != "*" && !strings.EqualFold(pm, method) {
		return false
	}
	ps := splitPath(strings.TrimSpace(pp))
	xs := splitPath(path)
	if len(ps) != len(xs) {
		return false
	}
	for i := range ps {
		if ps[i] != "*" && ps[i] != xs[i] {
			return false
		}
	}
	return true
}

func splitPath(p string) []string { return strings.Split(strings.Trim(p, "/"), "/") }

// Scope is the effective setting for one request.
type Scope struct {
	Endpoint        string // label used to group latency, for example "GET /orders/*"
	IgnoreFields    []string
	UnorderedArrays []string
}

// For returns the scope for a request: global settings plus the first matching endpoint rule.
func (r *Rules) For(method, path string) Scope {
	s := Scope{
		Endpoint:        method + " " + autoTemplate(path),
		IgnoreFields:    append([]string(nil), r.IgnoreFields...),
		UnorderedArrays: append([]string(nil), r.UnorderedArrays...),
	}
	for _, e := range r.Endpoints {
		if MatchPattern(e.Match, method, path) {
			s.Endpoint = strings.TrimSpace(e.Match)
			s.IgnoreFields = append(s.IgnoreFields, e.IgnoreFields...)
			s.UnorderedArrays = append(s.UnorderedArrays, e.UnorderedArrays...)
			break
		}
	}
	return s
}

// autoTemplate turns all-digit path segments into "*" so /products/1 and /products/2 share one latency bucket.
func autoTemplate(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if isDigits(p) {
			parts[i] = "*"
		}
	}
	return strings.Join(parts, "/")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
