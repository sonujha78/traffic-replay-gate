package recorder

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonujha78/traffic-replay-gate/internal/golden"
	"github.com/sonujha78/traffic-replay-gate/internal/mask"
)

func newFront(t *testing.T, target *url.URL) (*httptest.Server, string, *golden.Writer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "g.jsonl")
	w, err := golden.NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := New(Options{Target: target, Writer: w, Masker: mask.New(nil), Skip: []string{"/health"}})
	front := httptest.NewServer(rec)
	t.Cleanup(front.Close)
	return front, path, w
}

func TestRecordsMasksAndForwards(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusCreated)
		rw.Write([]byte(`{"id":7,"email":"asha@example.com","price":299.0}`))
	}))
	defer up.Close()
	target, _ := url.Parse(up.URL)
	front, path, w := newFront(t, target)

	req, _ := http.NewRequest("POST", front.URL+"/orders?token=abc", strings.NewReader(`{"password":"hunter2","qty":2}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (the proxy must forward untouched)", resp.StatusCode)
	}
	w.Close()

	recs, err := golden.ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(recs))
	}
	r := recs[0]
	if r.Method != "POST" || r.Path != "/orders" || r.Response.Status != 201 {
		t.Fatalf("unexpected record: %+v", r)
	}
	if r.Request.Headers["Authorization"] != mask.Placeholder {
		t.Fatalf("Authorization not masked: %q", r.Request.Headers["Authorization"])
	}
	if !strings.Contains(string(r.Response.Body), "299.0") {
		t.Fatalf("response body lost its number: %s", r.Response.Body)
	}

	raw, _ := os.ReadFile(path)
	for _, secret := range []string{"secret-token", "hunter2", "asha@example.com", "token=abc"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret %q leaked into the golden file", secret)
		}
	}
}

func TestSkipsHealthPath(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte("ok"))
	}))
	defer up.Close()
	target, _ := url.Parse(up.URL)
	front, path, w := newFront(t, target)

	for _, p := range []string{"/health", "/products"} {
		resp, err := http.Get(front.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	w.Close()

	recs, _ := golden.ReadAll(path)
	if len(recs) != 1 || recs[0].Path != "/products" {
		t.Fatalf("expected only /products to be recorded, got %+v", recs)
	}
}

func TestUpstreamDownIsNotRecorded(t *testing.T) {
	up := httptest.NewServer(http.NotFoundHandler())
	target, _ := url.Parse(up.URL)
	up.Close() // nothing is listening any more

	front, _, w := newFront(t, target)
	resp, err := http.Get(front.URL + "/products")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if w.Count() != 0 {
		t.Fatalf("a failed upstream call must not be recorded, got %d", w.Count())
	}
}
