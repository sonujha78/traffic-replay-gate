// Package compare finds the differences between a recorded response and a replayed one.
package compare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/sonujha78/traffic-replay-gate/internal/rules"
)

// Diff is one difference. Kind is one of the rules.Kind* names.
type Diff struct {
	Kind     string `json:"kind"`
	Where    string `json:"where"`
	Path     string `json:"path,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

type Options struct {
	// IgnoreFields are field paths such as "id", "user.email" or "items[].id".
	IgnoreFields []string
	// UnorderedArrays are array paths whose element order does not matter, such as "items".
	UnorderedArrays []string
}

// Decode parses JSON and keeps numbers as json.Number, so 299.0 is not rewritten.
func Decode(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// Status compares HTTP status codes.
func Status(expected, actual int) []Diff {
	if expected == actual {
		return nil
	}
	return []Diff{{Kind: rules.KindStatus, Where: "status",
		Expected: strconv.Itoa(expected), Actual: strconv.Itoa(actual)}}
}

// Headers compares only the named headers. Content-Type is compared without its parameters.
func Headers(expected, actual map[string]string, names []string) []Diff {
	var out []Diff
	for _, name := range names {
		ev, eok := lookup(expected, name)
		av, aok := lookup(actual, name)
		if !eok && !aok {
			continue
		}
		if eok && aok && normHeader(name, ev) == normHeader(name, av) {
			continue
		}
		if !eok {
			ev = "(absent)"
		}
		if !aok {
			av = "(absent)"
		}
		out = append(out, Diff{Kind: rules.KindHeader, Where: "header " + name, Expected: ev, Actual: av})
	}
	return out
}

func lookup(m map[string]string, name string) (string, bool) {
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

func normHeader(name, v string) string {
	v = strings.TrimSpace(v)
	if strings.EqualFold(name, "content-type") {
		v, _, _ = strings.Cut(v, ";")
		v = strings.ToLower(strings.TrimSpace(v))
	}
	return v
}

// Bodies compares two decoded JSON documents and lists every difference.
func Bodies(expected, actual any, opt Options) []Diff {
	d := &differ{ignore: set(opt.IgnoreFields), unordered: set(opt.UnorderedArrays)}
	d.walk("", "$", expected, actual)
	return d.out
}

type differ struct {
	ignore    map[string]bool
	unordered map[string]bool
	out       []Diff
}

func set(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		if s = strings.TrimSpace(s); s != "" {
			m[s] = true
		}
	}
	return m
}

func (d *differ) add(kind, path, exp, act string) {
	d.out = append(d.out, Diff{Kind: kind, Where: "body", Path: path, Expected: exp, Actual: act})
}

func join(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// walk compares e and a. rp is the rule path used for ignore rules ("items[].id"),
// dp is the display path shown in reports ("$.items[2].id").
func (d *differ) walk(rp, dp string, e, a any) {
	switch ev := e.(type) {
	case map[string]any:
		av, ok := a.(map[string]any)
		if !ok {
			d.typeDiff(dp, e, a)
			return
		}
		for _, k := range sortedKeys(ev) {
			krp := join(rp, k)
			if d.ignore[krp] {
				continue
			}
			kdp := dp + "." + k
			if _, present := av[k]; !present {
				d.add(rules.KindRemoved, kdp, short(ev[k]), "(missing)")
				continue
			}
			d.walk(krp, kdp, ev[k], av[k])
		}
		for _, k := range sortedKeys(av) {
			if _, present := ev[k]; present || d.ignore[join(rp, k)] {
				continue
			}
			d.add(rules.KindAdded, dp+"."+k, "(missing)", short(av[k]))
		}

	case []any:
		av, ok := a.([]any)
		if !ok {
			d.typeDiff(dp, e, a)
			return
		}
		erp := rp + "[]"
		left, right := ev, av
		if d.unordered[rp] {
			left, right = d.sorted(erp, ev), d.sorted(erp, av)
		}
		n := len(left)
		if len(right) < n {
			n = len(right)
		}
		for i := 0; i < n; i++ {
			d.walk(erp, fmt.Sprintf("%s[%d]", dp, i), left[i], right[i])
		}
		for i := n; i < len(left); i++ {
			d.add(rules.KindRemoved, fmt.Sprintf("%s[%d]", dp, i), short(left[i]), "(missing)")
		}
		for i := n; i < len(right); i++ {
			d.add(rules.KindAdded, fmt.Sprintf("%s[%d]", dp, i), "(missing)", short(right[i]))
		}

	case json.Number:
		av, ok := a.(json.Number)
		if !ok {
			d.typeDiff(dp, e, a)
			return
		}
		if !sameNumber(ev, av) {
			d.add(rules.KindValue, dp, string(ev), string(av))
		}

	case string:
		av, ok := a.(string)
		if !ok {
			d.typeDiff(dp, e, a)
			return
		}
		if ev != av {
			d.add(rules.KindValue, dp, short(ev), short(av))
		}

	case bool:
		av, ok := a.(bool)
		if !ok {
			d.typeDiff(dp, e, a)
			return
		}
		if ev != av {
			d.add(rules.KindValue, dp, short(ev), short(av))
		}

	case nil:
		if a != nil {
			d.typeDiff(dp, e, a)
		}

	default:
		if fmt.Sprint(e) != fmt.Sprint(a) {
			d.add(rules.KindValue, dp, short(e), short(a))
		}
	}
}

func (d *differ) typeDiff(dp string, e, a any) {
	d.add(rules.KindType, dp, typeName(e)+" "+short(e), typeName(a)+" "+short(a))
}

// sorted orders array elements by their content, ignoring ignored fields, so order does not matter.
func (d *differ) sorted(rp string, items []any) []any {
	type pair struct {
		key string
		val any
	}
	ps := make([]pair, len(items))
	for i, it := range items {
		b, _ := json.Marshal(d.strip(rp, it))
		ps[i] = pair{string(b), it}
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].key < ps[j].key })
	out := make([]any, len(ps))
	for i, p := range ps {
		out[i] = p.val
	}
	return out
}

// strip returns a copy of v without ignored fields.
func (d *differ) strip(rp string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			krp := join(rp, k)
			if d.ignore[krp] {
				continue
			}
			out[k] = d.strip(krp, val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = d.strip(rp+"[]", val)
		}
		return out
	}
	return v
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sameNumber(a, b json.Number) bool {
	if a == b {
		return true
	}
	fa, errA := strconv.ParseFloat(string(a), 64)
	fb, errB := strconv.ParseFloat(string(b), 64)
	return errA == nil && errB == nil && fa == fb
}

func typeName(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case json.Number:
		return "number"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

func short(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}

// LatencyRow compares the p95 latency of one endpoint between the baseline and the candidate.
type LatencyRow struct {
	Endpoint     string  `json:"endpoint"`
	Samples      int     `json:"samples"`
	BaselineP95  float64 `json:"baseline_p95_ms"`
	CandidateP95 float64 `json:"candidate_p95_ms"`
	IncreaseMS   float64 `json:"increase_ms"`
	IncreasePct  float64 `json:"increase_pct"`
	Regressed    bool    `json:"regressed"`
}

// Percentile returns the p-th percentile (nearest rank) of values.
func Percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	rank := int(math.Ceil(p / 100 * float64(len(s))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(s) {
		rank = len(s)
	}
	return s[rank-1]
}

// Latency compares p95 per endpoint. A regression needs BOTH the percentage limit and the
// absolute limit to be exceeded, so a jump from 1 ms to 2 ms on a fast endpoint is not flagged.
func Latency(baseline, candidate map[string][]float64, th rules.Latency) []LatencyRow {
	keys := make([]string, 0, len(baseline))
	for k := range baseline {
		if len(candidate[k]) > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	rows := make([]LatencyRow, 0, len(keys))
	for _, k := range keys {
		b, c := Percentile(baseline[k], 95), Percentile(candidate[k], 95)
		inc := c - b
		pct := inc / math.Max(b, 0.1) * 100
		rows = append(rows, LatencyRow{
			Endpoint:     k,
			Samples:      len(candidate[k]),
			BaselineP95:  round1(b),
			CandidateP95: round1(c),
			IncreaseMS:   round1(inc),
			IncreasePct:  round1(pct),
			Regressed:    pct > th.MaxP95IncreasePct && inc > th.MinIncreaseMS,
		})
	}
	return rows
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
