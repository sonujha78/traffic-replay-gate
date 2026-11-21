package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sonujha78/traffic-replay-gate/internal/golden"
	"github.com/sonujha78/traffic-replay-gate/internal/mask"
	"github.com/sonujha78/traffic-replay-gate/internal/rules"
)

func rec(method, path string, status int, body string) golden.Record {
	return golden.Record{
		Method: method,
		Path:   path,
		Response: golden.Response{
			Status:  status,
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    json.RawMessage(body),
		},
		LatencyMS: 1.0,
	}
}

func run(t *testing.T, target string, recs []golden.Record, r *rules.Rules, hdr map[string]string) *Report {
	t.Helper()
	rep, err := Run(context.Background(), Options{
		Target: target, Records: recs, Rules: r, Masker: mask.New(nil), Headers: hdr,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func kinds(rep *Report) []string {
	var out []string
	for _, r := range rep.Results {
		for _, f := range r.Findings {
			out = append(out, f.Kind)
		}
	}
	return out
}

func jsonServer(h func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		h(w, r)
	}))
	return srv
}

func TestStatefulReplayRemapsCapturedID(t *testing.T) {
	var mu sync.Mutex
	orders := map[string]bool{}
	n := 0
	srv := jsonServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/orders":
			n++
			id := fmt.Sprintf("new%06d", n)
			orders[id] = true
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"id":%q,"status":"created"}`, id)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/orders/"):
			id := strings.TrimPrefix(r.URL.Path, "/orders/")
			if !orders[id] {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"detail":"order not found"}`)
				return
			}
			fmt.Fprintf(w, `{"id":%q,"status":"created"}`, id)
		default:
			w.WriteHeader(404)
		}
	})
	defer srv.Close()

	post := rec("POST", "/orders", 201, `{"id":"old123456","status":"created"}`)
	post.Request.Headers = map[string]string{"Content-Type": "application/json"}
	post.Request.Body = json.RawMessage(`{"items":[{"product_id":1,"qty":1}]}`)
	get := rec("GET", "/orders/old123456", 200, `{"id":"old123456","status":"created"}`)

	rl := rules.Default()
	rl.Endpoints = []rules.Endpoint{
		{Match: "POST /orders", IgnoreFields: []string{"id"}, CaptureFields: []string{"id"}},
		{Match: "GET /orders/*", IgnoreFields: []string{"id"}},
	}

	first := run(t, srv.URL, []golden.Record{post, get}, rl, nil)
	if first.Verdict != "pass" || first.Fails != 0 || first.Warns != 0 || first.Clean != 2 {
		t.Fatalf("expected a clean pass, got %+v", first)
	}

	// Running the same replay again must give the same verdict.
	second := run(t, srv.URL, []golden.Record{post, get}, rl, nil)
	if second.Verdict != first.Verdict || second.Fails != first.Fails || second.Warns != first.Warns {
		t.Fatalf("replay is not repeatable: %+v vs %+v", first, second)
	}
}

func TestBreakingChangeFails(t *testing.T) {
	srv := jsonServer(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":1,"name":"USB-C Cable","price":"299.0"}`)
	})
	defer srv.Close()

	recs := []golden.Record{rec("GET", "/products/1", 200, `{"id":1,"name":"USB-C Cable","price":299.0,"stock":120}`)}
	rep := run(t, srv.URL, recs, rules.Default(), nil)
	if rep.Verdict != "fail" || rep.Fails != 2 {
		t.Fatalf("expected fail with 2 findings, got %+v", rep)
	}
	got := strings.Join(kinds(rep), ",")
	if !strings.Contains(got, rules.KindRemoved) || !strings.Contains(got, rules.KindType) {
		t.Fatalf("expected removed_field and type_change, got %s", got)
	}
}

func TestAddedFieldWarnsButPasses(t *testing.T) {
	srv := jsonServer(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":1,"price":299.0,"currency":"INR"}`)
	})
	defer srv.Close()

	recs := []golden.Record{rec("GET", "/products/1", 200, `{"id":1,"price":299.0}`)}
	rep := run(t, srv.URL, recs, rules.Default(), nil)
	if rep.Verdict != "pass" || rep.Fails != 0 || rep.Warns != 1 {
		t.Fatalf("expected pass with 1 warning, got %+v", rep)
	}
}

func TestMaskedCredentialsAreFilledInFromOptions(t *testing.T) {
	srv := jsonServer(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/me":
			if auth != "Bearer x" {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"detail":"no"}`)
				return
			}
			fmt.Fprint(w, `{"ok":true}`)
		case "/public":
			if auth != "" { // must not receive credentials it never had
				w.WriteHeader(400)
				return
			}
			fmt.Fprint(w, `{"ok":true}`)
		}
	})
	defer srv.Close()

	me := rec("GET", "/me", 200, `{"ok":true}`)
	me.Request.Headers = map[string]string{"Authorization": mask.Placeholder}
	pub := rec("GET", "/public", 200, `{"ok":true}`)
	recs := []golden.Record{me, pub}

	ok := run(t, srv.URL, recs, rules.Default(), map[string]string{"authorization": "Bearer x"})
	if ok.Verdict != "pass" {
		t.Fatalf("with credentials the replay should pass: %+v", ok)
	}
	bad := run(t, srv.URL, recs, rules.Default(), nil)
	if bad.Verdict != "fail" || !strings.Contains(strings.Join(kinds(bad), ","), rules.KindStatus) {
		t.Fatalf("without credentials /me must fail with a status change: %+v", bad)
	}
}

func TestLatencyRegressionFails(t *testing.T) {
	srv := jsonServer(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		fmt.Fprint(w, `{"ok":true}`)
	})
	defer srv.Close()

	rep := run(t, srv.URL, []golden.Record{rec("GET", "/slow", 200, `{"ok":true}`)}, rules.Default(), nil)
	if rep.Verdict != "fail" || len(rep.Latency) != 1 || !rep.Latency[0].Regressed {
		t.Fatalf("a 120 ms jump from 1 ms must fail: %+v", rep)
	}
	if rep.Fails != 1 {
		t.Fatalf("expected exactly the latency finding, got %d fails", rep.Fails)
	}
}

func TestUnreachableTargetFails(t *testing.T) {
	srv := jsonServer(func(w http.ResponseWriter, r *http.Request) {})
	url := srv.URL
	srv.Close()

	rep := run(t, url, []golden.Record{rec("GET", "/x", 200, `{}`)}, rules.Default(), nil)
	if rep.Verdict != "fail" || !strings.Contains(strings.Join(kinds(rep), ","), rules.KindRequestError) {
		t.Fatalf("an unreachable target must fail with request_error: %+v", rep)
	}
}
