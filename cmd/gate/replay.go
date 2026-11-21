package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sonujha78/traffic-replay-gate/internal/golden"
	"github.com/sonujha78/traffic-replay-gate/internal/mask"
	"github.com/sonujha78/traffic-replay-gate/internal/replay"
	"github.com/sonujha78/traffic-replay-gate/internal/report"
	"github.com/sonujha78/traffic-replay-gate/internal/rules"
)

type headerFlags []string

func (h *headerFlags) String() string     { return strings.Join(*h, ",") }
func (h *headerFlags) Set(v string) error { *h = append(*h, v); return nil }

// runReplay returns 0 for pass, 1 for fail and 2 for a usage or runtime error.
func runReplay(args []string) int {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	target := fs.String("target", "", "base URL of the candidate version (required)")
	goldenPath := fs.String("golden", "", "golden set file to replay (required)")
	rulesPath := fs.String("rules", "rules/default.yaml", "rules file")
	outDir := fs.String("out-dir", "reports/out", "directory for report.json and report.md")
	maskFields := fs.String("mask-fields", "", "extra JSON field names to mask, same as used when recording")
	warmup := fs.String("warmup-path", "/health", "path requested a few times before replaying (empty to disable)")
	timeout := fs.Duration("timeout", 30*time.Second, "timeout for each request")
	var headers headerFlags
	fs.Var(&headers, "header", "real value for a header that was masked at record time, as Name=Value (repeatable)")
	fs.Parse(args)

	if *target == "" || *goldenPath == "" {
		fmt.Fprintln(os.Stderr, "replay: --target and --golden are required")
		return 2
	}
	hdr := map[string]string{}
	for _, h := range headers {
		name, value, ok := strings.Cut(h, "=")
		if !ok || strings.TrimSpace(name) == "" {
			fmt.Fprintf(os.Stderr, "replay: bad --header %q, want Name=Value\n", h)
			return 2
		}
		hdr[strings.TrimSpace(name)] = value
	}
	rl, err := rules.Load(*rulesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay: %v\n", err)
		return 2
	}
	recs, err := golden.ReadAll(*goldenPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay: %v\n", err)
		return 2
	}
	if len(recs) == 0 {
		fmt.Fprintf(os.Stderr, "replay: golden set %s is empty\n", *goldenPath)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rep, err := replay.Run(ctx, replay.Options{
		Target:     *target,
		Records:    recs,
		Rules:      rl,
		Masker:     mask.New(splitCSV(*maskFields)),
		Headers:    hdr,
		Timeout:    *timeout,
		WarmupPath: *warmup,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay: %v\n", err)
		return 2
	}
	if err := report.WriteAll(*outDir, rep); err != nil {
		fmt.Fprintf(os.Stderr, "replay: %v\n", err)
		return 2
	}

	fmt.Print(report.Summary(rep))
	fmt.Printf("report written to %s\n", filepath.Join(*outDir, "report.md"))

	// Inside GitHub Actions the report also appears on the run's summary page.
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
			f.WriteString(report.Markdown(rep))
			f.Close()
		}
	}

	if rep.Verdict != "pass" {
		return 1
	}
	return 0
}
