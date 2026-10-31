package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/kuroishi/hsg/internal/domain"
)

func TestSummaryAggregation(t *testing.T) {
	r := New()
	r.Record(domain.RequestObservation{Kind: domain.KindPlaylist, URL: "a", Status: 200, Bytes: 1000, TTFB: 10 * time.Millisecond, Duration: 12 * time.Millisecond})
	r.Record(domain.RequestObservation{Kind: domain.KindSegment, URL: "s1", Status: 200, Bytes: 2000, TTFB: 20 * time.Millisecond, Duration: 100 * time.Millisecond})
	r.Record(domain.RequestObservation{Kind: domain.KindSegment, URL: "s2", Status: 200, Bytes: 3000, TTFB: 40 * time.Millisecond, Duration: 300 * time.Millisecond})
	r.Record(domain.RequestObservation{Kind: domain.KindSegment, URL: "s3", Status: 200, Bytes: 4000, TTFB: 60 * time.Millisecond, Duration: 500 * time.Millisecond})
	r.Record(domain.RequestObservation{Kind: domain.KindSegment, URL: "s4", Status: 500, Err: errors.New("http 500")})

	s := r.Summary(2*time.Second, 7)
	if s.WallSeconds != 2 || s.Clients != 7 {
		t.Fatalf("summary metadata wrong: %+v", s)
	}
	if s.TotalRequests != 5 || s.TotalErrors != 1 || s.TotalBytes != 10000 {
		t.Fatalf("totals wrong: %+v", s)
	}
	if want := 5000.0; s.ThroughputBps != want {
		t.Fatalf("throughput = %v, want %v", s.ThroughputBps, want)
	}
	if len(s.Kinds) != 2 {
		t.Fatalf("kinds = %d, want 2", len(s.Kinds))
	}

	seg := s.Kinds[0]
	if seg.Kind != "playlist" {
		t.Fatalf("expected kinds sorted; first is %q", seg.Kind)
	}
	seg = s.Kinds[1]
	if seg.Kind != "segment" {
		t.Fatalf("expected segment kind, got %q", seg.Kind)
	}
	if seg.Requests != 4 || seg.Errors != 1 || seg.Bytes != 9000 {
		t.Fatalf("segment totals wrong: %+v", seg)
	}
	if seg.TTFBSamples != 3 || seg.DurationSamples != 3 {
		t.Fatalf("failed observations must not feed the latency samples: %+v", seg)
	}
	// sorted: 20, 40, 60 → p50=40
	if seg.TTFBMs.P50 != 40 || seg.DurationMs.P50 != 300 {
		t.Fatalf("p50 wrong: %+v / %+v", seg.TTFBMs, seg.DurationMs)
	}
}

func TestProgressCountersMatchObservations(t *testing.T) {
	r := New()
	r.Record(domain.RequestObservation{Kind: domain.KindPlaylist, Status: 200, Bytes: 100})
	r.Record(domain.RequestObservation{Kind: domain.KindSegment, Status: 200, Bytes: 2000})
	r.Record(domain.RequestObservation{Kind: domain.KindSegment, Status: 500, Bytes: 10, Err: errors.New("http 500")})

	req, errs, bytes := r.Progress()
	if req != 3 || errs != 1 || bytes != 2110 {
		t.Fatalf("progress = (%d,%d,%d), want (3,1,2110)", req, errs, bytes)
	}
}

func TestSummaryEmpty(t *testing.T) {
	s := New().Summary(time.Second, 1)
	if s.TotalRequests != 0 || len(s.Kinds) != 0 || s.ThroughputBps != 0 {
		t.Fatalf("empty summary wrong: %+v", s)
	}
}
