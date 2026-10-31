package domain

import (
	"context"
	"net/url"
	"time"
)

// Request kinds recorded for every HTTP exchange. Playlist and segment
// requests are named after the track they belong to.
const (
	KindPlaylist         = "playlist"
	KindPlaylistAudio    = "playlist-audio"
	KindPlaylistSubtitle = "playlist-subtitle"
	KindSegment          = "segment"
	KindSegmentAudio     = "segment-audio"
	KindSegmentSubtitle  = "segment-subtitle"
	KindInit             = "init"
	KindInitAudio        = "init-audio"
	KindInitSubtitle     = "init-subtitle"
)

// GetOptions parameterizes a single (internally retried) HTTP GET.
type GetOptions struct {
	Kind    string        // one of the Kind* constants; used for metric aggregation
	Stream  bool          // when true the body is discarded (counted, not buffered)
	Retries int           // extra attempts after the first one
	Backoff time.Duration // base backoff; doubles with each retry
}

// RequestObservation describes one HTTP exchange as it was observed,
// successful or not. Implementations of MetricsRecorder aggregate these.
type RequestObservation struct {
	Kind     string
	URL      string
	Status   int // 0 when the request never got a response
	Bytes    int64
	TTFB     time.Duration // until the response headers arrived
	Duration time.Duration // until the body was fully consumed
	Err      error
}

// FetchResult carries a fully-buffered response body.
type FetchResult struct {
	Body []byte
}

// Fetcher performs HTTP GETs with the retry policy given in GetOptions and
// records one RequestObservation per attempt.
type Fetcher interface {
	Get(ctx context.Context, rawURL string, opts GetOptions) (*FetchResult, error)
}

// PlaylistParser parses raw M3U8 text into a master or a media playlist.
// Exactly one of the returned values is non-nil. URIs inside the result are
// resolved against base.
type PlaylistParser interface {
	Parse(text string, base *url.URL) (*MasterPlaylist, *MediaPlaylist, error)
}

// MetricsRecorder receives every observed HTTP exchange.
type MetricsRecorder interface {
	Record(o RequestObservation)
}

// Clock abstracts time for the playback pacing.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
}

// Sleeper waits for a duration (or until ctx is done).
type Sleeper interface {
	Sleep(ctx context.Context, d time.Duration) error
}

// Random is the source of jitter. Everything drawn from here is
// seed-reproducible.
type Random interface {
	Float64() float64 // uniform in [0,1)
}

// RetryPolicy is the retry/backoff policy shared by all requests of a run.
type RetryPolicy struct {
	MaxRetries int
	Backoff    time.Duration
}

// Attempts is the total number of tries: first try + retries.
func (p RetryPolicy) Attempts() int {
	if p.MaxRetries <= 0 {
		return 1
	}
	return 1 + p.MaxRetries
}

// BackoffFor returns the wait before the given attempt (1-based). Attempt 1
// is the first retry.
func (p RetryPolicy) BackoffFor(attempt int) time.Duration {
	base := p.Backoff
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	shift := attempt - 1
	if shift > 16 {
		shift = 16
	}
	if shift < 0 {
		shift = 0
	}
	return base << uint(shift)
}
