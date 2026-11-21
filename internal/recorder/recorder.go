// Package recorder is a reverse proxy that forwards traffic untouched and saves a masked copy.
package recorder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/sonujha78/traffic-replay-gate/internal/golden"
	"github.com/sonujha78/traffic-replay-gate/internal/mask"
)

const proxyErrHeader = "X-Trg-Proxy-Error"

// Headers that change on every call or describe the connection, not the service's behaviour.
var skipReqHeaders = map[string]bool{
	"user-agent": true, "accept-encoding": true, "content-length": true,
	"connection": true, "x-forwarded-for": true,
}

var skipRespHeaders = map[string]bool{
	"date": true, "content-length": true, "server": true, "connection": true,
	"keep-alive": true, strings.ToLower(proxyErrHeader): true,
}

type Options struct {
	Target  *url.URL
	Writer  *golden.Writer
	Masker  *mask.Masker
	Skip    []string // path prefixes that are forwarded but not recorded
	MaxBody int      // largest body stored, in bytes
	Logf    func(format string, args ...any)
}

type Recorder struct {
	opts  Options
	proxy *httputil.ReverseProxy
}

func New(o Options) *Recorder {
	if o.MaxBody <= 0 {
		o.MaxBody = 1 << 20
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	rp := httputil.NewSingleHostReverseProxy(o.Target)
	director := rp.Director
	rp.Director = func(r *http.Request) {
		director(r)
		r.Header.Del("Accept-Encoding") // ask for plain bodies so they can be stored readably
		r.Host = o.Target.Host
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		o.Logf("upstream error for %s %s: %v", r.Method, r.URL.Path, err)
		w.Header().Set(proxyErrHeader, "1")
		w.WriteHeader(http.StatusBadGateway)
	}
	return &Recorder{opts: o, proxy: rp}
}

func (rc *Recorder) Count() int64 { return rc.opts.Writer.Count() }

func (rc *Recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	reqBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(reqBody))
	r.ContentLength = int64(len(reqBody))

	cw := &capture{ResponseWriter: w}
	rc.proxy.ServeHTTP(cw, r)
	latency := time.Since(start)

	if rc.skipped(r.URL.Path) || cw.Header().Get(proxyErrHeader) != "" {
		return
	}
	rec := rc.build(r, reqBody, cw, latency)
	if err := rc.opts.Writer.Write(rec); err != nil {
		rc.opts.Logf("could not write record: %v", err)
		return
	}
	target := r.URL.Path
	if rec.Query != "" {
		target += "?" + rec.Query
	}
	rc.opts.Logf("#%d %s %s -> %d (%.1f ms)", rec.Seq, r.Method, target, rec.Response.Status, rec.LatencyMS)
}

func (rc *Recorder) skipped(path string) bool {
	for _, p := range rc.opts.Skip {
		if p != "" && strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func (rc *Recorder) build(r *http.Request, reqBody []byte, cw *capture, latency time.Duration) *golden.Record {
	status := cw.status
	if status == 0 {
		status = http.StatusOK
	}
	return &golden.Record{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  rc.opts.Masker.Query(r.URL.RawQuery),
		Request: golden.Request{
			Headers: rc.headers(r.Header, skipReqHeaders),
			Body:    rc.body(reqBody, r.Header.Get("Content-Type")),
		},
		Response: golden.Response{
			Status:  status,
			Headers: rc.headers(cw.Header(), skipRespHeaders),
			Body:    rc.body(cw.body.Bytes(), cw.Header().Get("Content-Type")),
		},
		LatencyMS:  float64(latency.Microseconds()) / 1000,
		RecordedAt: time.Now().UTC(),
	}
}

func (rc *Recorder) headers(h http.Header, skip map[string]bool) map[string]string {
	out := map[string]string{}
	for k, vals := range h {
		if skip[strings.ToLower(k)] {
			continue
		}
		out[k] = rc.opts.Masker.Header(k, strings.Join(vals, ", "))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// body stores JSON bodies as JSON (masked) and everything else as a string.
func (rc *Recorder) body(b []byte, contentType string) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	if len(b) > rc.opts.MaxBody {
		out, _ := json.Marshal(fmt.Sprintf("<omitted: %d bytes>", len(b)))
		return out
	}
	if strings.Contains(strings.ToLower(contentType), "json") {
		if out, ok := rc.opts.Masker.JSON(b); ok {
			return out
		}
	}
	out, _ := json.Marshal(string(b))
	return out
}

// capture copies what the proxy writes to the client so it can be recorded.
type capture struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *capture) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *capture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.body.Write(b)
	return c.ResponseWriter.Write(b)
}

func (c *capture) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *capture) Unwrap() http.ResponseWriter { return c.ResponseWriter }
