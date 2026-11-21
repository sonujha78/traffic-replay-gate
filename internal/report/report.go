// Package report turns a replay result into text, Markdown and JSON.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sonujha78/traffic-replay-gate/internal/replay"
	"github.com/sonujha78/traffic-replay-gate/internal/rules"
)

const maxListed = 50 // requests listed in the Markdown report; the JSON always has everything

var indexRe = regexp.MustCompile(`\[\d+\]`)

// collapse turns "$.items[3].price" into "$.items[].price".
func collapse(path string) string { return indexRe.ReplaceAllString(path, "[]") }

// grouped is one or more findings of the same kind on the same field, differing only by array index.
type grouped struct {
	replay.Finding
	Count int
	norm  string
}

// path is the real path for a single finding and the collapsed path for a repeated one.
func (g grouped) path() string {
	if g.Count > 1 {
		return g.norm
	}
	return g.Path
}

func group(fs []replay.Finding) []grouped {
	var out []grouped
	pos := map[string]int{}
	for _, f := range fs {
		norm := collapse(f.Path)
		key := string(f.Severity) + "|" + f.Kind + "|" + f.Where + "|" + norm
		if i, ok := pos[key]; ok {
			out[i].Count++
			continue
		}
		pos[key] = len(out)
		out = append(out, grouped{Finding: f, Count: 1, norm: norm})
	}
	return out
}

// distinct counts the different problems across the whole run. The same problem on
// /products and on /products?category=audio is one issue, not two.
func distinct(rep *replay.Report) (fails, warns int) {
	seen := map[string]bool{}
	for _, r := range rep.Results {
		for _, f := range r.Findings {
			key := r.Endpoint + "|" + string(f.Severity) + "|" + f.Kind + "|" + f.Where + "|" + collapse(f.Path)
			if seen[key] {
				continue
			}
			seen[key] = true
			if f.Severity == rules.Fail {
				fails++
			} else {
				warns++
			}
		}
	}
	for _, l := range rep.Latency {
		switch l.Severity {
		case rules.Fail:
			fails++
		case rules.Warn:
			warns++
		}
	}
	return fails, warns
}

// WriteAll writes report.json and report.md into dir.
func WriteAll(dir string, rep *replay.Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(Markdown(rep)), 0o644)
}

// code wraps text for a Markdown table cell without letting it break the table.
func code(s string) string {
	if s == "" {
		return ""
	}
	s = strings.NewReplacer("|", "\\|", "`", "'", "\n", " ", "\r", " ").Replace(s)
	return "`" + s + "`"
}

// Markdown is the human-readable report, also used as the pull request comment.
func Markdown(rep *replay.Report) string {
	var b strings.Builder
	df, dw := distinct(rep)
	fmt.Fprintf(&b, "# Traffic Replay Gate: %s\n\n", strings.ToUpper(rep.Verdict))
	fmt.Fprintf(&b, "Target `%s`. Replayed **%d** requests, **%d** clean. Failing findings: **%d** (**%d** distinct issues). Warnings: **%d** (**%d** distinct issues).\n\n",
		rep.Target, rep.Total, rep.Clean, rep.Fails, df, rep.Warns, dw)

	withFindings := 0
	for _, r := range rep.Results {
		if len(r.Findings) > 0 {
			withFindings++
		}
	}
	if withFindings > 0 {
		b.WriteString("## Differences\n\n")
		b.WriteString("Repeated differences on array items are grouped (`[]` means any index). When Count is above 1, Expected and Actual show the first occurrence.\n\n")
		listed := 0
		for _, r := range rep.Results {
			if len(r.Findings) == 0 {
				continue
			}
			if listed == maxListed {
				fmt.Fprintf(&b, "_%d more requests with differences are in report.json._\n\n", withFindings-listed)
				break
			}
			listed++
			target := r.Path
			if r.Query != "" {
				target += "?" + r.Query
			}
			fmt.Fprintf(&b, "### #%d %s %s\n\n", r.Seq, r.Method, target)
			b.WriteString("| Severity | Kind | Where | Expected | Actual | Count |\n|---|---|---|---|---|---|\n")
			for _, g := range group(r.Findings) {
				where := g.Where
				if p := g.path(); p != "" {
					where += " " + code(p)
				}
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %d |\n",
					g.Severity, g.Kind, where, code(g.Expected), code(g.Actual), g.Count)
			}
			b.WriteString("\n")
		}
	}

	if len(rep.Latency) > 0 {
		b.WriteString("## Latency (p95, ms)\n\n")
		b.WriteString("| Endpoint | Samples | Baseline | Candidate | Change | Result |\n|---|---|---|---|---|---|\n")
		for _, l := range rep.Latency {
			res := "ok"
			if l.Severity != "" {
				res = string(l.Severity)
			}
			fmt.Fprintf(&b, "| %s | %d | %.1f | %.1f | %+.1f ms (%+.0f%%) | %s |\n",
				code(l.Endpoint), l.Samples, l.BaselineP95, l.CandidateP95, l.IncreaseMS, l.IncreasePct, res)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Summary is the short text printed to the terminal. Failing items come first.
func Summary(rep *replay.Report) string {
	var b strings.Builder
	df, _ := distinct(rep)
	fmt.Fprintf(&b, "VERDICT: %s (%d requests, %d clean, %d failing findings in %d distinct issues, %d warnings)\n",
		strings.ToUpper(rep.Verdict), rep.Total, rep.Clean, rep.Fails, df, rep.Warns)

	var failLines, warnLines []string
	for _, r := range rep.Results {
		for _, g := range group(r.Findings) {
			suffix := ""
			if g.Count > 1 {
				suffix = fmt.Sprintf(" (x%d)", g.Count)
			}
			line := fmt.Sprintf("  [%s] #%d %s %s  %s %s  expected %s, actual %s%s",
				g.Severity, r.Seq, r.Method, r.Path, g.Kind, g.path(), g.Expected, g.Actual, suffix)
			if g.Severity == rules.Fail {
				failLines = append(failLines, line)
			} else {
				warnLines = append(warnLines, line)
			}
		}
	}
	for _, l := range rep.Latency {
		if l.Severity == "" {
			continue
		}
		line := fmt.Sprintf("  [%s] latency %s  p95 %.1f -> %.1f ms (%+.1f ms)",
			l.Severity, l.Endpoint, l.BaselineP95, l.CandidateP95, l.IncreaseMS)
		if l.Severity == rules.Fail {
			failLines = append(failLines, line)
		} else {
			warnLines = append(warnLines, line)
		}
	}

	lines := append(failLines, warnLines...)
	for i, l := range lines {
		if i == 30 {
			fmt.Fprintf(&b, "  ... and %d more (see report.md)\n", len(lines)-30)
			break
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}
