package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonujha78/traffic-replay-gate/internal/compare"
	"github.com/sonujha78/traffic-replay-gate/internal/replay"
	"github.com/sonujha78/traffic-replay-gate/internal/rules"
)

func sample() *replay.Report {
	return &replay.Report{
		Target: "http://localhost:18000", Total: 2, Clean: 1, Fails: 2, Verdict: "fail",
		Results: []replay.Result{{
			Seq: 5, Method: "GET", Path: "/products/1", Endpoint: "GET /products/*",
			Findings: []replay.Finding{{
				Diff:     compare.Diff{Kind: "removed_field", Where: "body", Path: "$.stock", Expected: "120", Actual: "a|b"},
				Severity: rules.Fail,
			}},
		}},
		Latency: []replay.LatencyFinding{{
			LatencyRow: compare.LatencyRow{Endpoint: "GET /products", Samples: 4, BaselineP95: 1.2,
				CandidateP95: 250, IncreaseMS: 248.8, IncreasePct: 20733, Regressed: true},
			Severity: rules.Fail,
		}},
	}
}

func TestMarkdown(t *testing.T) {
	md := Markdown(sample())
	for _, want := range []string{"# Traffic Replay Gate: FAIL", "removed_field", "$.stock", "GET /products", "Latency"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown is missing %q:\n%s", want, md)
		}
	}
	if !strings.Contains(md, `a\|b`) {
		t.Errorf("pipes inside cells must be escaped:\n%s", md)
	}
}

func TestSummaryListsFailuresFirst(t *testing.T) {
	s := Summary(sample())
	if !strings.HasPrefix(s, "VERDICT: FAIL") {
		t.Fatalf("unexpected summary:\n%s", s)
	}
	if !strings.Contains(s, "removed_field") || !strings.Contains(s, "latency GET /products") {
		t.Fatalf("summary is missing findings:\n%s", s)
	}
}

func TestWriteAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	if err := WriteAll(dir, sample()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"report.json", "report.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s was not written: %v", f, err)
		}
	}
}
