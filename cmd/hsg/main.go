// Command hsg load-tests HLS streaming servers by emulating concurrent
// viewing clients and reporting latency/throughput metrics.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kuroishi/hsg/internal/domain"
	"github.com/kuroishi/hsg/internal/infra/httpfetch"
	"github.com/kuroishi/hsg/internal/infra/m3u8"
	"github.com/kuroishi/hsg/internal/infra/metrics"
	"github.com/kuroishi/hsg/internal/infra/progress"
	"github.com/kuroishi/hsg/internal/infra/rng"
	"github.com/kuroishi/hsg/internal/infra/timex"
	"github.com/kuroishi/hsg/internal/usecase"
)

const defaultBackoff = 500 * time.Millisecond

func newHTTPClient(forceHTTP1 bool) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 16
	tr.IdleConnTimeout = 90 * time.Second
	if forceHTTP1 {
		tr.ForceAttemptHTTP2 = false
		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = &tls.Config{}
		}
	}
	return &http.Client{Transport: tr}
}

type options struct {
	clients      int
	rendition    int
	audioLang    string
	subtitleLang string
	duration     time.Duration
	liveRefresh  time.Duration
	retries      int
	timeout      time.Duration
	jsonOut      bool
	http1        bool
	seed         int64
	quiet        bool
}

func parseFlags() (*options, string, error) {
	o := &options{}
	flag.IntVar(&o.clients, "clients", 1, "number of virtual clients")
	flag.IntVar(&o.rendition, "rendition", -1, "rendition index in the master playlist (0-based); default: highest bandwidth")
	flag.StringVar(&o.audioLang, "audio-lang", "", "audio track language or NAME to select; default: DEFAULT=YES track")
	flag.StringVar(&o.subtitleLang, "subtitle-lang", "", "subtitle track language or NAME; default: DEFAULT=YES track")
	flag.DurationVar(&o.duration, "duration", 0, "playback duration (required for LIVE playlists)")
	flag.DurationVar(&o.liveRefresh, "live-refresh", 0, "LIVE playlist refresh interval; default: playlist TARGETDURATION")
	flag.IntVar(&o.retries, "retries", 3, "retries per HTTP request (429/5xx/network errors; exponential backoff)")
	flag.DurationVar(&o.timeout, "timeout", 0, "overall run timeout (off when omitted)")
	flag.BoolVar(&o.jsonOut, "json", false, "print the summary as JSON")
	flag.BoolVar(&o.http1, "http1", false, "force HTTP/1.1")
	flag.Int64Var(&o.seed, "seed", 0, "random seed for reproducible jitter (0 = time-based)")
	flag.BoolVar(&o.quiet, "quiet", false, "suppress the progress display (summary is still printed)")
	flag.BoolVar(&o.quiet, "q", false, "shorthand for --quiet")
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintln(out, "hsg emulates concurrent HLS clients to load-test streaming servers.")
		fmt.Fprintln(out, "\nUsage: hsg [flags] <master-or-media-playlist-url>\n\nFlags:")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		return nil, "", errors.New("exactly one playlist URL is required")
	}
	if o.clients < 1 {
		return nil, "", errors.New("--clients must be >= 1")
	}
	if o.retries < 0 {
		return nil, "", errors.New("--retries must be >= 0")
	}
	return o, flag.Arg(0), nil
}

func main() {
	os.Exit(run())
}

func run() int {
	opts, playlistURL, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		flag.Usage()
		return 2
	}

	rec := metrics.New()
	parser := m3u8.New()
	start := time.Now()
	clientsDone := &atomic.Int64{}

	ctx := context.Background()
	if opts.timeout > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, opts.timeout)
		defer timeoutCancel()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Live progress line on stderr (never on stdout). Rewritten in place on
	// a terminal; 10s snapshots when stderr is redirected; off with --quiet.
	progCtx, progCancel := context.WithCancel(context.Background())
	defer progCancel()
	if !opts.quiet {
		stderr, _ := os.Stderr.Stat()
		isTTY := stderr.Mode()&os.ModeCharDevice != 0
		interval, rewrite := 10*time.Second, false
		if isTTY {
			interval, rewrite = time.Second, true
		}
		display := progress.New(os.Stderr, interval, rewrite)
		go display.Run(progCtx, progressSource{rec: rec, clients: opts.clients, done: clientsDone, start: start})
	}

	errs := make([]error, opts.clients)
	var wg sync.WaitGroup
	for i := 0; i < opts.clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer clientsDone.Add(1)
			runner := usecase.NewClientRunner(
				httpfetch.New(httpfetch.Options{
					Client:   newHTTPClient(opts.http1),
					Recorder: rec,
					Sleeper:  timex.Sleeper{},
				}),
				parser,
				timex.Clock{},
				timex.Sleeper{},
				rng.New(opts.seed, i),
			)
			errs[i] = runner.Run(ctx, usecase.Config{
				ID:             i,
				URL:            playlistURL,
				RenditionIndex: opts.rendition,
				AudioLang:      opts.audioLang,
				SubtitleLang:   opts.subtitleLang,
				PlayDuration:   opts.duration,
				LiveRefresh:    opts.liveRefresh,
				Retry:          domain.RetryPolicy{MaxRetries: opts.retries, Backoff: defaultBackoff},
				JitterFraction: 0.1,
			})
		}(i)
	}
	wg.Wait()
	// Close the progress line before the summary lands on stdout.
	progCancel()
	wall := time.Since(start)

	// Report identical client failures once, with a client count.
	failures := map[string]int{}
	var fatal error
	for _, err := range errs {
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		failures[err.Error()]++
		if fatal == nil {
			fatal = err
		}
	}
	for msg, n := range failures {
		fmt.Fprintf(os.Stderr, "error (%d client(s)): %s\n", n, msg)
	}

	summary := rec.Summary(wall, opts.clients)
	if opts.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)
	} else {
		printTextSummary(os.Stdout, summary)
	}

	if fatal != nil {
		return 1
	}
	return 0
}

func printTextSummary(w io.Writer, s *metrics.Summary) {
	fmt.Fprintf(w, "wall: %s | clients: %d | requests: %d | errors: %d | bytes: %s | throughput: %s/s\n\n",
		s.Wall.Round(time.Millisecond), s.Clients, s.TotalRequests, s.TotalErrors,
		humanBytes(s.TotalBytes), humanBytes(int64(s.ThroughputBps)))
	fmt.Fprintf(w, "%-18s %8s %7s %12s | %26s | %26s\n",
		"kind", "reqs", "errors", "bytes", "ttfb p50/p95/p99 (ms)", "dur p50/p95/p99 (ms)")
	for _, k := range s.Kinds {
		fmt.Fprintf(w, "%-18s %8d %7d %12s | %26s | %26s\n",
			k.Kind, k.Requests, k.Errors, humanBytes(k.Bytes),
			fmtMs(k.TTFBMs), fmtMs(k.DurationMs))
	}
}

// progressSource adapts the run state to progress.Stats.
type progressSource struct {
	rec     *metrics.Recorder
	clients int
	done    *atomic.Int64
	start   time.Time
}

func (s progressSource) Stats() progress.Stats {
	requests, errors, bytes := s.rec.Progress()
	return progress.Stats{
		Clients:     s.clients,
		ClientsDone: int(s.done.Load()),
		Requests:    requests,
		Errors:      errors,
		Bytes:       bytes,
		Elapsed:     time.Since(s.start),
	}
}

func fmtMs(p metrics.Percentiles) string {
	return fmt.Sprintf("%8.0f/%8.0f/%8.0f", p.P50, p.P95, p.P99)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
