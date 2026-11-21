// Package replay sends a golden set to a candidate version and compares every answer.
package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sonujha78/traffic-replay-gate/internal/compare"
	"github.com/sonujha78/traffic-replay-gate/internal/golden"
	"github.com/sonujha78/traffic-replay-gate/internal/mask"
	"github.com/sonujha78/traffic-replay-gate/internal/rules"
)

type Options struct {
	Target     string // base URL of the candidate version
	Records    []golden.Record
	Rules      *rules.Rules
	Masker     *mask.Masker      // masks live responses the same way the recorder masked the golden ones
	Headers    map[string]string // real values for headers that were masked at record time
	Timeout    time.Duration
	WarmupPath string // GET this a few times first so a cold start does not look like a regression
	Client     *http.Client
}

// Finding is one difference together with how serious the rules say it is.
type Finding struct {
	compare.Diff
	Severity rules.Severity `json:"severity"`
}

type Result struct {
	Seq      int64     `json:"seq"`
	Method   string    `json:"method"`
	Path     string    `json:"path"`
	Query    string    `json:"query,omitempty"`
	Endpoint string    `json:"endpoint"`
	Status   int       `json:"status"` // status of the replayed response, 0 if the request failed
	Findings []Finding `json:"findings,omitempty"`
}

type LatencyFinding struct {
	compare.LatencyRow
	Severity rules.Severity `json:"severity,omitempty"` // empty means within limits
}

type Report struct {
	Target    string           `json:"target"`
	StartedAt time.Time        `json:"started_at"`
	Total     int              `json:"total"`
	Clean     int              `json:"clean"` // requests with no findings at all
	Fails     int              `json:"fails"` // findings that block the release
	Warns     int              `json:"warns"` // findings that are only reported
	Verdict   string           `json:"verdict"`
	Results   []Result         `json:"results"`
	Latency   []LatencyFinding `json:"latency"`
}

type reply struct {
	status    int
	headers   map[string]string
	body      []byte
	latencyMS float64
}

type runner struct {
	o      Options
	base   string
	client *http.Client
}

// Run replays every record in order and returns the report. The verdict is "fail" if any
// finding has severity fail, otherwise "pass".
func Run(ctx context.Context, o Options) (*Report, error) {
	if o.Rules == nil {
		o.Rules = rules.Default()
	}
	if o.Masker == nil {
		o.Masker = mask.New(nil)
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	u, err := url.Parse(o.Target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("bad target %q", o.Target)
	}
	client := o.Client
	if client == nil {
		client = &http.Client{
			Timeout: o.Timeout,
			// Redirects are compared as recorded, not followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	r := &runner{o: o, base: strings.TrimRight(o.Target, "/"), client: client}
	r.warmup(ctx)

	rep := &Report{Target: o.Target, StartedAt: time.Now().UTC(), Total: len(o.Records)}
	baseLat := map[string][]float64{}
	candLat := map[string][]float64{}
	idMap := map[string]string{} // recorded value -> value the candidate produced

	for i := range o.Records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rec := &o.Records[i]
		scope := o.Rules.For(rec.Method, rec.Path)
		res := Result{Seq: rec.Seq, Method: rec.Method, Path: rec.Path, Query: rec.Query, Endpoint: scope.Endpoint}

		var diffs []compare.Diff
		got, err := r.send(ctx, rec, idMap)
		if err != nil {
			diffs = []compare.Diff{{Kind: rules.KindRequestError, Where: "request", Expected: "a response", Actual: err.Error()}}
		} else {
			res.Status = got.status
			baseLat[scope.Endpoint] = append(baseLat[scope.Endpoint], rec.LatencyMS)
			candLat[scope.Endpoint] = append(candLat[scope.Endpoint], got.latencyMS)
			diffs = r.diff(rec, got, scope)
			r.learn(rec, got, scope, idMap)
		}

		for _, d := range diffs {
			sev := o.Rules.SeverityOf(d.Kind)
			if sev == rules.Ignore {
				continue
			}
			res.Findings = append(res.Findings, Finding{Diff: d, Severity: sev})
			if sev == rules.Fail {
				rep.Fails++
			} else {
				rep.Warns++
			}
		}
		if len(res.Findings) == 0 {
			rep.Clean++
		}
		rep.Results = append(rep.Results, res)
	}

	for _, row := range compare.Latency(baseLat, candLat, o.Rules.Latency) {
		lf := LatencyFinding{LatencyRow: row}
		if row.Regressed && o.Rules.Latency.Severity != rules.Ignore {
			lf.Severity = o.Rules.Latency.Severity
			if lf.Severity == rules.Fail {
				rep.Fails++
			} else {
				rep.Warns++
			}
		}
		rep.Latency = append(rep.Latency, lf)
	}

	rep.Verdict = "pass"
	if rep.Fails > 0 {
		rep.Verdict = "fail"
	}
	return rep, nil
}

func (r *runner) warmup(ctx context.Context) {
	if r.o.WarmupPath == "" {
		return
	}
	for i := 0; i < 3; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+r.o.WarmupPath, nil)
		if err != nil {
			return
		}
		resp, err := r.client.Do(req)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func (r *runner) send(ctx context.Context, rec *golden.Record, idMap map[string]string) (*reply, error) {
	target := r.base + substitute(rec.Path, idMap)
	if q := substitute(rec.Query, idMap); q != "" {
		target += "?" + q
	}
	var body io.Reader
	if b := requestBody(rec, idMap); len(b) > 0 {
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, rec.Method, target, body)
	if err != nil {
		return nil, err
	}
	for k, v := range rec.Request.Headers {
		if v == mask.Placeholder {
			// Masked at record time: fill in the real value only where the original request had this header.
			if real, ok := headerLookup(r.o.Headers, k); ok {
				req.Header.Set(k, real)
			}
			continue
		}
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	latency := time.Since(start)

	hdrs := map[string]string{}
	for k, vals := range resp.Header {
		hdrs[k] = strings.Join(vals, ", ")
	}
	return &reply{
		status:    resp.StatusCode,
		headers:   hdrs,
		body:      data,
		latencyMS: float64(latency.Microseconds()) / 1000,
	}, nil
}

func (r *runner) diff(rec *golden.Record, got *reply, scope rules.Scope) []compare.Diff {
	diffs := compare.Status(rec.Response.Status, got.status)
	diffs = append(diffs, compare.Headers(rec.Response.Headers, got.headers, r.o.Rules.CompareHeaders)...)
	return append(diffs, r.bodyDiff(rec, got, scope)...)
}

func (r *runner) bodyDiff(rec *golden.Record, got *reply, scope rules.Scope) []compare.Diff {
	expRaw := bytes.TrimSpace([]byte(rec.Response.Body))
	gotRaw := bytes.TrimSpace(got.body)
	switch {
	case len(expRaw) == 0 && len(gotRaw) == 0:
		return nil
	case len(expRaw) == 0:
		return []compare.Diff{{Kind: rules.KindAdded, Where: "body", Path: "$", Expected: "(empty)", Actual: preview(gotRaw)}}
	case len(gotRaw) == 0:
		return []compare.Diff{{Kind: rules.KindRemoved, Where: "body", Path: "$", Expected: preview(expRaw), Actual: "(empty)"}}
	}

	exp, err := compare.Decode(expRaw)
	if err != nil {
		return []compare.Diff{{Kind: rules.KindValue, Where: "body", Path: "$", Expected: "(unreadable golden body)", Actual: err.Error()}}
	}

	// Same rule the recorder used: JSON bodies are masked and parsed, everything else is a string.
	var act any = string(got.body)
	if strings.Contains(strings.ToLower(headerValue(got.headers, "Content-Type")), "json") {
		if masked, ok := r.o.Masker.JSON(got.body); ok {
			if v, err := compare.Decode(masked); err == nil {
				act = v
			}
		}
	}
	return compare.Bodies(exp, act, compare.Options{
		IgnoreFields:    scope.IgnoreFields,
		UnorderedArrays: scope.UnorderedArrays,
	})
}

// learn remembers recorded -> actual values for the endpoint's capture_fields.
func (r *runner) learn(rec *golden.Record, got *reply, scope rules.Scope, idMap map[string]string) {
	if len(scope.CaptureFields) == 0 {
		return
	}
	exp, err := compare.Decode([]byte(rec.Response.Body))
	if err != nil {
		return
	}
	act, err := compare.Decode(got.body)
	if err != nil {
		return
	}
	for _, f := range scope.CaptureFields {
		ev, ok1 := lookupString(exp, f)
		av, ok2 := lookupString(act, f)
		if ok1 && ok2 && len(ev) >= 4 && ev != av {
			idMap[ev] = av
		}
	}
}

func lookupString(v any, path string) (string, bool) {
	for _, part := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		v, ok = m[part]
		if !ok {
			return "", false
		}
	}
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return string(t), true
	}
	return "", false
}

// substitute replaces recorded values with the values the candidate produced.
func substitute(s string, idMap map[string]string) string {
	if len(idMap) == 0 || s == "" {
		return s
	}
	keys := make([]string, 0, len(idMap))
	for k := range idMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s = strings.ReplaceAll(s, k, idMap[k])
	}
	return s
}

func requestBody(rec *golden.Record, idMap map[string]string) []byte {
	raw := []byte(rec.Request.Body)
	if len(raw) == 0 {
		return nil
	}
	if !strings.Contains(strings.ToLower(headerValue(rec.Request.Headers, "Content-Type")), "json") {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			raw = []byte(s)
		}
	}
	return []byte(substitute(string(raw), idMap))
}

func headerLookup(h map[string]string, name string) (string, bool) {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

func headerValue(h map[string]string, name string) string {
	v, _ := headerLookup(h, name)
	return v
}

func preview(b []byte) string {
	s := string(b)
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}
