// Package httpfetch implements the domain.Fetcher port on top of net/http,
// including the per-request retry loop and one observation per attempt.
package httpfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kuroishi/hsg/internal/domain"
	"github.com/kuroishi/hsg/internal/infra/timex"
)

const (
	defaultUserAgent = "hsg/0.1.0"
	defaultBackoff   = 500 * time.Millisecond
)

// Options wires a Fetcher to its collaborators.
type Options struct {
	Client    *http.Client           // per virtual client, connection-isolated
	Recorder  domain.MetricsRecorder // shared across clients
	Sleeper   domain.Sleeper         // used for retry backoff
	UserAgent string
}

// Fetcher performs GETs; every attempt is reported to the recorder.
type Fetcher struct {
	client   *http.Client
	recorder domain.MetricsRecorder
	sleeper  domain.Sleeper
	ua       string
}

// New builds a Fetcher.
func New(opts Options) *Fetcher {
	if opts.Client == nil {
		opts.Client = http.DefaultClient
	}
	if opts.Sleeper == nil {
		opts.Sleeper = timex.Sleeper{}
	}
	if opts.UserAgent == "" {
		opts.UserAgent = defaultUserAgent
	}
	return &Fetcher{
		client:   opts.Client,
		recorder: opts.Recorder,
		sleeper:  opts.Sleeper,
		ua:       opts.UserAgent,
	}
}

// Get implements domain.Fetcher. Retryable failures (429, 5xx, network,
// mid-body read errors) are retried with exponential backoff; other 4xx
// fail on the first attempt. The observation of each attempt is recorded.
func (f *Fetcher) Get(ctx context.Context, rawURL string, o domain.GetOptions) (*domain.FetchResult, error) {
	retries := o.Retries
	if retries < 0 {
		retries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			policy := domain.RetryPolicy{MaxRetries: retries, Backoff: o.Backoff}
			if err := f.sleep(ctx, policy.BackoffFor(attempt)); err != nil {
				return nil, err
			}
		}
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", f.ua)
		resp, err := f.client.Do(req)
		if err != nil {
			lastErr = err
			f.record(o, rawURL, 0, 0, 0, time.Since(start), err)
			continue
		}
		ttfb := time.Since(start)
		var (
			body    []byte
			n       int64
			readErr error
		)
		if o.Stream {
			n, readErr = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		} else {
			body, readErr = io.ReadAll(resp.Body)
			resp.Body.Close()
			n = int64(len(body))
		}
		dur := time.Since(start)
		if resp.StatusCode >= 400 {
			statusErr := fmt.Errorf("http %d for %s", resp.StatusCode, rawURL)
			f.record(o, rawURL, resp.StatusCode, n, ttfb, dur, statusErr)
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				lastErr = statusErr
				continue
			}
			return nil, statusErr
		}
		f.record(o, rawURL, resp.StatusCode, n, ttfb, dur, readErr)
		if readErr != nil {
			lastErr = readErr
			continue
		}
		return &domain.FetchResult{Body: body}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("request failed")
	}
	return nil, lastErr
}

func (f *Fetcher) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	return f.sleeper.Sleep(ctx, d)
}

func (f *Fetcher) record(o domain.GetOptions, url string, status int, n int64, ttfb, dur time.Duration, err error) {
	if f.recorder == nil {
		return
	}
	f.recorder.Record(domain.RequestObservation{
		Kind:     o.Kind,
		URL:      url,
		Status:   status,
		Bytes:    n,
		TTFB:     ttfb,
		Duration: dur,
		Err:      err,
	})
}
