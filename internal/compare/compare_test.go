package compare

import (
	"testing"

	"github.com/sonujha78/traffic-replay-gate/internal/rules"
)

func doc(t *testing.T, s string) any {
	t.Helper()
	v, err := Decode([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func one(t *testing.T, diffs []Diff, kind, path string) Diff {
	t.Helper()
	if len(diffs) != 1 {
		t.Fatalf("got %d diffs, want 1: %+v", len(diffs), diffs)
	}
	if diffs[0].Kind != kind || diffs[0].Path != path {
		t.Fatalf("got %s at %q, want %s at %q", diffs[0].Kind, diffs[0].Path, kind, path)
	}
	return diffs[0]
}

func TestAddedFieldIsReportedAsAdded(t *testing.T) {
	d := Bodies(doc(t, `{"a":1}`), doc(t, `{"a":1,"currency":"INR"}`), Options{})
	one(t, d, rules.KindAdded, "$.currency")
}

func TestRemovedField(t *testing.T) {
	d := Bodies(doc(t, `{"name":"x","stock":5}`), doc(t, `{"name":"x"}`), Options{})
	one(t, d, rules.KindRemoved, "$.stock")
}

func TestTypeChangeNumberToString(t *testing.T) {
	d := Bodies(doc(t, `{"price":299.0}`), doc(t, `{"price":"299.0"}`), Options{})
	got := one(t, d, rules.KindType, "$.price")
	if got.Expected != "number 299.0" || got.Actual != `string "299.0"` {
		t.Fatalf("unexpected description: %+v", got)
	}
}

func TestNumbersCompareByValue(t *testing.T) {
	if d := Bodies(doc(t, `{"price":299.0}`), doc(t, `{"price":299}`), Options{}); len(d) != 0 {
		t.Fatalf("299.0 and 299 should be equal: %+v", d)
	}
}

func TestValueChange(t *testing.T) {
	d := Bodies(doc(t, `{"status":"created"}`), doc(t, `{"status":"pending"}`), Options{})
	one(t, d, rules.KindValue, "$.status")
}

func TestIgnoredFieldsAreSkipped(t *testing.T) {
	e := doc(t, `{"id":"x","created_at":"t1","items":[{"id":1,"n":2}]}`)
	a := doc(t, `{"id":"y","created_at":"t2","items":[{"id":9,"n":2}]}`)
	opt := Options{IgnoreFields: []string{"id", "created_at", "items[].id"}}
	if d := Bodies(e, a, opt); len(d) != 0 {
		t.Fatalf("noise should be ignored: %+v", d)
	}
}

func TestIgnoreIsScopedNotGlobal(t *testing.T) {
	e := doc(t, `{"id":"x","items":[{"id":1,"n":2}]}`)
	a := doc(t, `{"id":"y","items":[{"id":9,"n":2}]}`)
	// Ignoring the top-level id must not hide the change inside items.
	d := Bodies(e, a, Options{IgnoreFields: []string{"id"}})
	got := one(t, d, rules.KindValue, "$.items[0].id")
	if got.Expected != "1" || got.Actual != "9" {
		t.Fatalf("unexpected values: %+v", got)
	}
}

func TestIgnoredAddedFieldIsNotReported(t *testing.T) {
	d := Bodies(doc(t, `{"a":1}`), doc(t, `{"a":1,"trace":"z"}`), Options{IgnoreFields: []string{"trace"}})
	if len(d) != 0 {
		t.Fatalf("ignored added field was reported: %+v", d)
	}
}

func TestUnorderedArrays(t *testing.T) {
	e := doc(t, `{"tags":["b","a"]}`)
	a := doc(t, `{"tags":["a","b"]}`)
	if d := Bodies(e, a, Options{UnorderedArrays: []string{"tags"}}); len(d) != 0 {
		t.Fatalf("order should not matter: %+v", d)
	}
	if d := Bodies(e, a, Options{}); len(d) != 2 {
		t.Fatalf("order should matter by default, got %d diffs: %+v", len(d), d)
	}
}

func TestUnorderedArraysIgnoreNoiseWhenSorting(t *testing.T) {
	e := doc(t, `{"items":[{"id":1,"n":"b"},{"id":2,"n":"a"}]}`)
	a := doc(t, `{"items":[{"id":7,"n":"a"},{"id":8,"n":"b"}]}`)
	opt := Options{UnorderedArrays: []string{"items"}, IgnoreFields: []string{"items[].id"}}
	if d := Bodies(e, a, opt); len(d) != 0 {
		t.Fatalf("expected no diffs: %+v", d)
	}
}

func TestArrayLengthChange(t *testing.T) {
	one(t, Bodies(doc(t, `{"n":[1,2,3]}`), doc(t, `{"n":[1,2]}`), Options{}), rules.KindRemoved, "$.n[2]")
	one(t, Bodies(doc(t, `{"n":[1,2]}`), doc(t, `{"n":[1,2,3]}`), Options{}), rules.KindAdded, "$.n[2]")
}

func TestStatus(t *testing.T) {
	if len(Status(200, 200)) != 0 {
		t.Fatal("same status must not differ")
	}
	d := Status(404, 200)
	if len(d) != 1 || d[0].Kind != rules.KindStatus || d[0].Expected != "404" || d[0].Actual != "200" {
		t.Fatalf("unexpected: %+v", d)
	}
}

func TestHeaders(t *testing.T) {
	names := []string{"Content-Type"}
	same := Headers(map[string]string{"Content-Type": "application/json"},
		map[string]string{"content-type": "application/json; charset=utf-8"}, names)
	if len(same) != 0 {
		t.Fatalf("media type is the same: %+v", same)
	}
	diff := Headers(map[string]string{"Content-Type": "application/json"},
		map[string]string{"Content-Type": "text/html"}, names)
	if len(diff) != 1 || diff[0].Kind != rules.KindHeader {
		t.Fatalf("unexpected: %+v", diff)
	}
}

func TestPercentile(t *testing.T) {
	v := []float64{10, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	if got := Percentile(v, 95); got != 10 {
		t.Fatalf("p95 = %v, want 10", got)
	}
	if got := Percentile(v, 50); got != 5 {
		t.Fatalf("p50 = %v, want 5", got)
	}
	if got := Percentile(nil, 95); got != 0 {
		t.Fatalf("empty = %v, want 0", got)
	}
}

var limits = rules.Latency{MaxP95IncreasePct: 20, MinIncreaseMS: 30, Severity: rules.Fail}

func TestLatencyRegressionIsCaught(t *testing.T) {
	rows := Latency(
		map[string][]float64{"GET /products": {1.1, 1.2, 1.3}},
		map[string][]float64{"GET /products": {250, 255, 251}}, limits)
	if len(rows) != 1 || !rows[0].Regressed {
		t.Fatalf("a 250 ms jump must be flagged: %+v", rows)
	}
}

func TestLatencyNoiseOnFastEndpointIsNotFlagged(t *testing.T) {
	rows := Latency(
		map[string][]float64{"GET /products/*": {1.2, 1.3, 1.4}},
		map[string][]float64{"GET /products/*": {1.8, 1.9, 2.0}}, limits)
	if len(rows) != 1 || rows[0].Regressed {
		t.Fatalf("a 0.6 ms change must not be flagged: %+v", rows)
	}
}

func TestLatencySkipsEndpointsMissingOnOneSide(t *testing.T) {
	rows := Latency(map[string][]float64{"GET /a": {1}}, map[string][]float64{"GET /b": {1}}, limits)
	if len(rows) != 0 {
		t.Fatalf("expected no rows: %+v", rows)
	}
}
