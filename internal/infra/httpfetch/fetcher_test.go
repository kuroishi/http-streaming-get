package httpfetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kuroishi/hsg/internal/domain"
)

type nopSleeper struct{}

func (nopSleeper) Sleep(context.Context, time.Duration) error { return nil }

type fakeRecorder struct {
	mu  sync.Mutex
	obs []domain.RequestObservation
}

func (r *fakeRecorder) Record(o domain.RequestObservation) {
	r.mu.Lock()
	r.obs = append(r.obs, o)
	r.mu.Unlock()
}

func TestRetriesOn5xxThenSucceeds(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	f := New(Options{Recorder: rec, Sleeper: nopSleeper{}})
	res, err := f.Get(context.Background(), srv.URL, domain.GetOptions{
		Kind:    domain.KindPlaylist,
		Retries: 3,
		Backoff: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("want success after retries: %v", err)
	}
	if string(res.Body) != "ok" {
		t.Fatalf("body = %q", res.Body)
	}
	mu.Lock()
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	mu.Unlock()
	if got := len(rec.obs); got != 3 {
		t.Fatalf("observations = %d, want 3 (one per attempt)", got)
	}
	if rec.obs[0].Err == nil || rec.obs[1].Err == nil {
		t.Fatal("failed attempts must carry their error")
	}
	if rec.obs[2].Status != 200 || rec.obs[2].Err != nil {
		t.Fatalf("final observation wrong: %+v", rec.obs[2])
	}
	if rec.obs[2].Bytes != 2 {
		t.Fatalf("bytes = %d, want 2", rec.obs[2].Bytes)
	}
	if rec.obs[2].URL != srv.URL {
		t.Fatalf("url = %q", rec.obs[2].URL)
	}
}

func TestDoesNotRetryOnClientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	f := New(Options{Recorder: rec, Sleeper: nopSleeper{}})
	_, err := f.Get(context.Background(), srv.URL, domain.GetOptions{
		Kind:    domain.KindPlaylist,
		Retries: 5,
		Backoff: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want 404 error, got %v", err)
	}
	if got := len(rec.obs); got != 1 {
		t.Fatalf("observations = %d, want 1 (no retry on 404)", got)
	}
}

func TestRetriesNetworkErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens here anymore

	rec := &fakeRecorder{}
	f := New(Options{Recorder: rec, Sleeper: nopSleeper{}})
	_, err := f.Get(context.Background(), url, domain.GetOptions{
		Kind:    domain.KindSegment,
		Stream:  true,
		Retries: 2,
		Backoff: time.Millisecond,
	})
	if err == nil {
		t.Fatal("want a network error")
	}
	if got := len(rec.obs); got != 3 {
		t.Fatalf("observations = %d, want 3 (1 + 2 retries)", got)
	}
	for _, o := range rec.obs {
		if o.Err == nil {
			t.Fatal("network failures must be recorded with their error")
		}
		if o.Kind != domain.KindSegment {
			t.Fatalf("kind = %q", o.Kind)
		}
	}
}

func TestStreamDiscardsBodyButCountsBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Write(make([]byte, 128))
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	f := New(Options{Recorder: rec, Sleeper: nopSleeper{}})
	res, err := f.Get(context.Background(), srv.URL, domain.GetOptions{
		Kind:   domain.KindSegment,
		Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Body) != 0 {
		t.Fatal("streamed responses must not buffer the body")
	}
	if rec.obs[0].Bytes != 128 {
		t.Fatalf("bytes = %d, want 128", rec.obs[0].Bytes)
	}
}

func TestContextCancellationStopsRetrying(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	f := New(Options{Recorder: rec, Sleeper: nopSleeper{}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.Get(ctx, srv.URL, domain.GetOptions{Kind: domain.KindPlaylist, Retries: 5, Backoff: time.Second})
	if err == nil {
		t.Fatal("want an error when the context is canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
