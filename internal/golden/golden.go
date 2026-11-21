// Package golden defines the recorded request/response format and reads and writes it as JSON lines.
package golden

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type Request struct {
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type Record struct {
	Seq        int64     `json:"seq"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Query      string    `json:"query,omitempty"`
	Request    Request   `json:"request"`
	Response   Response  `json:"response"`
	LatencyMS  float64   `json:"latency_ms"`
	RecordedAt time.Time `json:"recorded_at"`
}

// Writer appends records to a file. It assigns Seq under the lock so file order matches Seq.
type Writer struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
	n   int64
}

func NewWriter(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return &Writer{f: f, enc: enc}, nil
}

func (w *Writer) Write(r *Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n++
	r.Seq = w.n
	return w.enc.Encode(r)
}

func (w *Writer) Count() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

func (w *Writer) Close() error { return w.f.Close() }

// ReadAll loads a golden set from a JSON lines file.
func ReadAll(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("record %d: %w", len(out)+1, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}
