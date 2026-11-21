package report

import (
	"fmt"
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

func repeated(n int) *replay.Report {
	var fs []replay.Finding
	for i := 0; i < n; i++ {
		fs = append(fs, replay.Finding{
			Diff: compare.Diff{Kind: "type_change", Where: "body",
				Path: fmt.Sprintf("$.items[%d].price", i), Expected: "number 1", Actual: `string "1"`},
			Severity: rules.Fail,
		})
	}
	return &replay.Report{
		Target: "t", Total: 1, Fails: n, Verdict: "fail",
		Results: []replay.Result{{Seq: 1, Method: "GET", Path: "/products", Endpoint: "GET /products", Findings: fs}},
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

func TestRepeatedFindingsAreGrouped(t *testing.T) {
	rep := repeated(3)
	md := Markdown(rep)
	if !strings.Contains(md, "$.items[].price") {
		t.Fatalf("expected the collapsed path:\n%s", md)
	}
	if strings.Contains(md, "$.items[0]") || strings.Contains(md, "$.items[1]") {
		t.Fatalf("indexed paths should be grouped away:\n%s", md)
	}
	if !strings.Contains(md, "| 3 |") {
		t.Fatalf("expected a Count of 3:\n%s", md)
	}
	if !strings.Contains(md, "**3** (**1** distinct issues)") {
		t.Fatalf("3 findings should be 1 distinct issue:\n%s", md)
	}
	if s := Summary(rep); !strings.Contains(s, "(x3)") || !strings.Contains(s, "1 distinct issues") {
		t.Fatalf("summary should show the group:\n%s", s)
	}
}

func TestSingleFindingKeepsItsRealPath(t *testing.T) {
	md := Markdown(repeated(1))
	if !strings.Contains(md, "$.items[0].price") {
		t.Fatalf("a single finding should keep its real path:\n%s", md)
	}
}

func TestDistinctCountsSameIssueOnceAcrossRequests(t *testing.T) {
	rep := repeated(2)
	second := rep.Results[0]
	second.Seq = 2
	second.Query = "category=audio" // another request to the same endpoint, same problem
	rep.Results = append(rep.Results, second)
	rep.Fails = 4
	if fails, _ := distinct(rep); fails != 1 {
		t.Fatalf("distinct fails = %d, want 1", fails)
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
