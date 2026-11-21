package rules

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern, method, path string
		want                  bool
	}{
		{"GET /orders/*", "GET", "/orders/abc", true},
		{"GET /orders/*", "GET", "/orders", false},
		{"GET /orders/*", "POST", "/orders/abc", false},
		{"POST /orders", "POST", "/orders", true},
		{"* /products", "GET", "/products", true},
		{"GET /a/*/c", "GET", "/a/b/c", true},
		{"GET /a/*/c", "GET", "/a/b/d", false},
	}
	for _, c := range cases {
		if got := MatchPattern(c.pattern, c.method, c.path); got != c.want {
			t.Errorf("MatchPattern(%q, %s, %s) = %v, want %v", c.pattern, c.method, c.path, got, c.want)
		}
	}
}

func TestLoadKeepsDefaultsForUnsetValues(t *testing.T) {
	p := write(t, "latency:\n  max_p95_increase_pct: 10\n")
	r, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Latency.MaxP95IncreasePct != 10 {
		t.Fatalf("pct = %v, want 10", r.Latency.MaxP95IncreasePct)
	}
	if r.Latency.MinIncreaseMS != 30 {
		t.Fatalf("min ms = %v, want the default 30", r.Latency.MinIncreaseMS)
	}
	if r.SeverityOf(KindStatus) != Fail || r.SeverityOf(KindAdded) != Warn {
		t.Fatal("default policy was lost")
	}
}

func TestLoadOverridesOnePolicyEntry(t *testing.T) {
	r, err := Load(write(t, "policy:\n  added_field: fail\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r.SeverityOf(KindAdded) != Fail {
		t.Fatal("override was not applied")
	}
	if r.SeverityOf(KindRemoved) != Fail {
		t.Fatal("the other defaults must stay")
	}
}

func TestLoadRejectsMistakes(t *testing.T) {
	bad := map[string]string{
		"unknown policy kind": "policy:\n  status_chnage: fail\n",
		"bad severity":        "policy:\n  status_change: maybe\n",
		"unknown key":         "polcy:\n  status_change: fail\n",
		"bad endpoint":        "endpoints:\n  - match: \"orders\"\n",
	}
	for name, content := range bad {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestForScope(t *testing.T) {
	r := Default()
	r.IgnoreFields = []string{"request_id"}
	r.Endpoints = []Endpoint{{Match: "GET /orders/*", IgnoreFields: []string{"id", "created_at"}}}

	s := r.For("GET", "/orders/abc123")
	if s.Endpoint != "GET /orders/*" {
		t.Fatalf("endpoint = %q", s.Endpoint)
	}
	if len(s.IgnoreFields) != 3 {
		t.Fatalf("ignore fields = %v, want global + endpoint", s.IgnoreFields)
	}
	if got := r.For("GET", "/products/3").Endpoint; got != "GET /products/*" {
		t.Fatalf("auto template = %q", got)
	}
	if got := r.For("GET", "/products").Endpoint; got != "GET /products" {
		t.Fatalf("plain path = %q", got)
	}
}
