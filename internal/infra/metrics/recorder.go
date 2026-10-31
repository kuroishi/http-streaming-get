// Package metrics implements the domain.MetricsRecorder port: it stores
// every observed HTTP exchange and aggregates them into a run summary.
package metrics

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kuroishi/hsg/internal/domain"
)

// Recorder collects observations from all virtual clients.
type Recorder struct {
	mu  sync.Mutex
	obs []domain.RequestObservation

	// O(1) run counters for the progress display, updated alongside the
	// stored observations.
	requests atomic.Int64
	errors   atomic.Int64
	bytes    atomic.Int64
}

// New returns an empty Recorder.
func New() *Recorder { return &Recorder{} }

// Record implements domain.MetricsRecorder. Safe for concurrent use.
func (r *Recorder) Record(o domain.RequestObservation) {
	r.mu.Lock()
	r.obs = append(r.obs, o)
	r.mu.Unlock()
	r.requests.Add(1)
	r.bytes.Add(o.Bytes)
	if o.Err != nil {
		r.errors.Add(1)
	}
}

// Progress returns the O(1) run counters for the progress display:
// total requests, total errors, total bytes.
func (r *Recorder) Progress() (requests, errors, bytes int64) {
	return r.requests.Load(), r.errors.Load(), r.bytes.Load()
}

// Percentiles in milliseconds.
type Percentiles struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
}

// KindStats aggregates the observations of one request kind.
type KindStats struct {
	Kind            string      `json:"kind"`
	Requests        int         `json:"requests"`
	Errors          int         `json:"errors"`
	Bytes           int64       `json:"bytes"`
	TTFBSamples     int         `json:"ttfb_samples"`
	DurationSamples int         `json:"duration_samples"`
	TTFBMs          Percentiles `json:"ttfb_ms"`
	DurationMs      Percentiles `json:"duration_ms"`
}

// Summary is the whole-run result.
type Summary struct {
	WallSeconds   float64       `json:"wall_seconds"`
	Wall          time.Duration `json:"-"`
	Clients       int           `json:"clients"`
	TotalRequests int           `json:"total_requests"`
	TotalErrors   int           `json:"total_errors"`
	TotalBytes    int64         `json:"total_bytes"`
	ThroughputBps float64       `json:"throughput_bytes_per_sec"`
	Kinds         []KindStats   `json:"kinds"`
}

// Summary aggregates all recorded observations. Failed exchanges only
// count towards errors; latency percentiles are computed over successful
// exchanges.
func (r *Recorder) Summary(wall time.Duration, clients int) *Summary {
	r.mu.Lock()
	all := make([]domain.RequestObservation, len(r.obs))
	copy(all, r.obs)
	r.mu.Unlock()

	type acc struct {
		stats    KindStats
		ttfb     []float64
		duration []float64
	}
	byKind := map[string]*acc{}
	for _, o := range all {
		a := byKind[o.Kind]
		if a == nil {
			a = &acc{}
			a.stats.Kind = o.Kind
			byKind[o.Kind] = a
		}
		a.stats.Requests++
		a.stats.Bytes += o.Bytes
		if o.Err != nil {
			a.stats.Errors++
			continue
		}
		if o.TTFB > 0 {
			a.ttfb = append(a.ttfb, ms(o.TTFB))
		}
		if o.Duration > 0 {
			a.duration = append(a.duration, ms(o.Duration))
		}
	}

	s := &Summary{Wall: wall, WallSeconds: wall.Seconds(), Clients: clients}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		a := byKind[k]
		a.stats.TTFBSamples = len(a.ttfb)
		a.stats.DurationSamples = len(a.duration)
		a.stats.TTFBMs = percentiles(a.ttfb)
		a.stats.DurationMs = percentiles(a.duration)
		s.Kinds = append(s.Kinds, a.stats)
		s.TotalRequests += a.stats.Requests
		s.TotalErrors += a.stats.Errors
		s.TotalBytes += a.stats.Bytes
	}
	if wall > 0 {
		s.ThroughputBps = float64(s.TotalBytes) / wall.Seconds()
	}
	return s
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func percentiles(v []float64) Percentiles {
	if len(v) == 0 {
		return Percentiles{}
	}
	sorted := append([]float64(nil), v...)
	sort.Float64s(sorted)
	pick := func(q float64) float64 {
		i := int(q * float64(len(sorted)-1))
		return sorted[i]
	}
	return Percentiles{P50: pick(0.50), P95: pick(0.95), P99: pick(0.99)}
}
