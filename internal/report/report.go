// Package report turns a replay result into text, Markdown and JSON.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sonujha78/traffic-replay-gate/internal/replay"
)

const maxListed = 50 // requests listed in the Markdown report; the JSON always has everything

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
	fmt.Fprintf(&b, "# Traffic Replay Gate: %s\n\n", strings.ToUpper(rep.Verdict))
	fmt.Fprintf(&b, "Target `%s`. Replayed **%d** requests: **%d** clean, **%d** failing, **%d** warnings.\n\n",
		rep.Target, rep.Total, rep.Clean, rep.Fails, rep.Warns)

	withFindings := 0
	for _, r := range rep.Results {
		if len(r.Findings) > 0 {
			withFindings++
		}
	}
	if withFindings > 0 {
		b.WriteString("## Differences\n\n")
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
			b.WriteString("| Severity | Kind | Where | Expected | Actual |\n|---|---|---|---|---|\n")
			for _, f := range r.Findings {
				where := f.Where
				if f.Path != "" {
					where += " " + code(f.Path)
				}
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", f.Severity, f.Kind, where, code(f.Expected), code(f.Actual))
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
	fmt.Fprintf(&b, "VERDICT: %s (%d requests, %d clean, %d failing, %d warnings)\n",
		strings.ToUpper(rep.Verdict), rep.Total, rep.Clean, rep.Fails, rep.Warns)

	var failLines, warnLines []string
	for _, r := range rep.Results {
		for _, f := range r.Findings {
			line := fmt.Sprintf("  [%s] #%d %s %s  %s %s  expected %s, actual %s",
				f.Severity, r.Seq, r.Method, r.Path, f.Kind, f.Path, f.Expected, f.Actual)
			if f.Severity == "fail" {
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
		if l.Severity == "fail" {
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
