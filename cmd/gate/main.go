package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sonujha78/traffic-replay-gate/internal/golden"
	"github.com/sonujha78/traffic-replay-gate/internal/mask"
	"github.com/sonujha78/traffic-replay-gate/internal/recorder"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "record":
		os.Exit(runRecord(os.Args[2:]))
	case "version":
		fmt.Println("gate", version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: gate <command> [flags]

commands:
  record   run a recording proxy in front of a service and save a golden set
  version  print the version`)
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func runRecord(args []string) int {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	listen := fs.String("listen", ":18080", "address the recording proxy listens on")
	target := fs.String("target", "", "base URL of the service to record (required)")
	out := fs.String("out", "golden/recorded.jsonl", "golden set file to write")
	maskFields := fs.String("mask-fields", "", "extra JSON field names to mask, comma separated")
	skip := fs.String("skip", "/health", "path prefixes that are not recorded, comma separated")
	maxBody := fs.Int("max-body", 1<<20, "largest body stored, in bytes")
	fs.Parse(args)

	if *target == "" {
		fmt.Fprintln(os.Stderr, "record: --target is required")
		return 2
	}
	u, err := url.Parse(*target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		fmt.Fprintf(os.Stderr, "record: bad --target %q\n", *target)
		return 2
	}

	w, err := golden.NewWriter(*out)
	if err != nil {
		log.Printf("record: %v", err)
		return 1
	}
	defer w.Close()

	rec := recorder.New(recorder.Options{
		Target:  u,
		Writer:  w,
		Masker:  mask.New(splitCSV(*maskFields)),
		Skip:    splitCSV(*skip),
		MaxBody: *maxBody,
		Logf:    log.Printf,
	})
	srv := &http.Server{Addr: *listen, Handler: rec, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
		close(done)
	}()

	log.Printf("recording %s on %s, writing %s", u, *listen, *out)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("record: %v", err)
		return 1
	}
	<-done
	log.Printf("stopped: %d requests recorded to %s", w.Count(), *out)
	return 0
}
