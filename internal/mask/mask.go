// Package mask hides sensitive values before anything is stored.
package mask

import (
	"bytes"
	"encoding/json"
	"strings"
)

const Placeholder = "***MASKED***"

var defaultHeaders = []string{"authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key"}

var defaultFields = []string{
	"password", "passwd", "token", "access_token", "refresh_token", "secret",
	"api_key", "apikey", "authorization", "email", "phone", "ssn", "card_number", "cvv",
}

type Masker struct {
	headers map[string]bool
	fields  map[string]bool
}

// New returns a Masker with the default rules plus any extra JSON field names.
func New(extraFields []string) *Masker {
	m := &Masker{headers: map[string]bool{}, fields: map[string]bool{}}
	for _, h := range defaultHeaders {
		m.headers[h] = true
	}
	for _, f := range defaultFields {
		m.fields[f] = true
	}
	for _, f := range extraFields {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			m.fields[f] = true
		}
	}
	return m
}

// Header returns the value, or the placeholder if the header is sensitive.
func (m *Masker) Header(name, value string) string {
	if m.headers[strings.ToLower(name)] {
		return Placeholder
	}
	return value
}

// Query masks sensitive parameters in a raw query string and keeps the original order.
func (m *Masker) Query(raw string) string {
	if raw == "" {
		return raw
	}
	parts := strings.Split(raw, "&")
	for i, p := range parts {
		key, _, found := strings.Cut(p, "=")
		if found && m.fields[strings.ToLower(key)] {
			parts[i] = key + "=" + Placeholder
		}
	}
	return strings.Join(parts, "&")
}

// JSON masks sensitive fields anywhere in a JSON document. Numbers keep their original text
// (299.0 stays 299.0). The second result is false if raw is not valid JSON.
func (m *Masker) JSON(raw []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	v = m.walk(v)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, false
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), true
}

func (m *Masker) walk(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if m.fields[strings.ToLower(k)] {
				t[k] = Placeholder
			} else {
				t[k] = m.walk(val)
			}
		}
		return t
	case []any:
		for i := range t {
			t[i] = m.walk(t[i])
		}
		return t
	}
	return v
}
