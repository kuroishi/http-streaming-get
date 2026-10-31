// Package usecase implements the virtual HLS client: it orchestrates
// playlist fetching, track selection and the VOD/EVENT/LIVE playback state
// machine, depending only on domain ports.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kuroishi/hsg/internal/domain"
)

// Config describes one virtual client run.
type Config struct {
	ID             int
	URL            string // master or media playlist URL
	RenditionIndex int    // -1 = highest bandwidth; otherwise position in master
	AudioLang      string // language or NAME; empty = DEFAULT=YES
	SubtitleLang   string
	PlayDuration   time.Duration // required for LIVE playlists
	LiveRefresh    time.Duration // 0 = playlist target duration
	Retry          domain.RetryPolicy
	JitterFraction float64 // fraction of pacing/refresh intervals, 0..0.5-ish
}

// ClientRunner emulates a single viewing client.
type ClientRunner struct {
	fetcher domain.Fetcher
	parser  domain.PlaylistParser
	clock   domain.Clock
	sleeper domain.Sleeper
	random  domain.Random
}

// NewClientRunner wires the runner to its ports.
func NewClientRunner(f domain.Fetcher, p domain.PlaylistParser, c domain.Clock, s domain.Sleeper, rnd domain.Random) *ClientRunner {
	return &ClientRunner{fetcher: f, parser: p, clock: c, sleeper: s, random: rnd}
}

// trackJob is one concurrently played track of a client (video, audio or
// subtitle).
type trackJob struct {
	baseURL *url.URL
	pl      *domain.MediaPlaylist
	plKind  string
	segKind string
}

// Run plays the whole session for one virtual client.
func (r *ClientRunner) Run(ctx context.Context, cfg Config) error {
	if cfg.URL == "" {
		return errors.New("usecase: playlist URL is required")
	}
	// Spread client startups so they do not sync perfectly.
	if d := time.Duration(r.random.Float64() * float64(time.Second)); d > 0 {
		if err := r.sleeper.Sleep(ctx, d); err != nil {
			return err
		}
	}
	base, err := url.Parse(cfg.URL)
	if err != nil {
		return fmt.Errorf("invalid playlist URL: %w", err)
	}
	res, err := r.fetcher.Get(ctx, cfg.URL, r.fetchOpts(domain.KindPlaylist, cfg))
	if err != nil {
		return err
	}
	master, media, err := r.parser.Parse(string(res.Body), base)
	if err != nil {
		return err
	}
	if media != nil {
		// The caller passed a media playlist directly: play it as the
		// single track.
		return r.runTracks(ctx, cfg, []trackJob{{
			baseURL: base, pl: media, plKind: domain.KindPlaylist, segKind: domain.KindSegment,
		}})
	}
	video, err := domain.SelectRendition(master, cfg.RenditionIndex)
	if err != nil {
		return err
	}
	jobs, err := r.collectTracks(ctx, cfg, master, video)
	if err != nil {
		return err
	}
	return r.runTracks(ctx, cfg, jobs)
}

// collectTracks fetches the video playlist plus the selected audio and
// subtitle playlists. Auxiliary tracks whose playlist cannot be fetched or
// parsed are skipped rather than failing the client.
func (r *ClientRunner) collectTracks(ctx context.Context, cfg Config, master *domain.MasterPlaylist, video *domain.Rendition) ([]trackJob, error) {
	playlist, base, err := r.fetchMediaPlaylist(ctx, cfg, video.URI, domain.KindPlaylist)
	if err != nil {
		return nil, err
	}
	jobs := []trackJob{{baseURL: base, pl: playlist, plKind: domain.KindPlaylist, segKind: domain.KindSegment}}

	if track, ok := domain.SelectMediaTrack(master, video, "AUDIO", cfg.AudioLang); ok {
		if pl, base, aerr := r.fetchMediaPlaylist(ctx, cfg, track.URI, domain.KindPlaylistAudio); aerr == nil {
			jobs = append(jobs, trackJob{baseURL: base, pl: pl, plKind: domain.KindPlaylistAudio, segKind: domain.KindSegmentAudio})
		}
	}
	if track, ok := domain.SelectMediaTrack(master, video, "SUBTITLES", cfg.SubtitleLang); ok {
		if pl, base, serr := r.fetchMediaPlaylist(ctx, cfg, track.URI, domain.KindPlaylistSubtitle); serr == nil {
			jobs = append(jobs, trackJob{baseURL: base, pl: pl, plKind: domain.KindPlaylistSubtitle, segKind: domain.KindSegmentSubtitle})
		}
	}
	return jobs, nil
}

func (r *ClientRunner) fetchMediaPlaylist(ctx context.Context, cfg Config, rawURL, kind string) (*domain.MediaPlaylist, *url.URL, error) {
	res, err := r.fetcher.Get(ctx, rawURL, r.fetchOpts(kind, cfg))
	if err != nil {
		return nil, nil, err
	}
	base, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid playlist URL: %w", err)
	}
	_, media, err := r.parser.Parse(string(res.Body), base)
	if err != nil {
		return nil, nil, err
	}
	if media == nil {
		return nil, nil, errors.New("expected a media playlist but found a master playlist")
	}
	return media, base, nil
}

// fetchOpts derives the GET options from the client retry policy.
func (r *ClientRunner) fetchOpts(kind string, cfg Config) domain.GetOptions {
	return domain.GetOptions{Kind: kind, Retries: cfg.Retry.MaxRetries, Backoff: cfg.Retry.Backoff}
}

// runTracks plays all tracks concurrently; the first track failure cancels
// the rest. Cancellation and timeouts initiated by the caller are not
// reported as failures.
func (r *ClientRunner) runTracks(ctx context.Context, cfg Config, jobs []trackJob) error {
	trackCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for _, j := range jobs {
		j := j
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := r.playPlaylist(trackCtx, cfg, j)
			if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if firstErr == nil {
				firstErr = fmt.Errorf("client %d: %s: %w", cfg.ID, j.segKind, err)
				cancel()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func initKind(segKind string) string {
	return strings.Replace(segKind, "segment", "init", 1)
}

// playPlaylist is the per-track playback loop. Segments are fetched
// sequentially at real-time pace; for LIVE/EVENT the playlist is refreshed
// after its segments are exhausted.
func (r *ClientRunner) playPlaylist(ctx context.Context, cfg Config, j trackJob) error {
	pl := j.pl
	mode := pl.Mode()
	if mode == domain.ModeLive && cfg.PlayDuration <= 0 {
		return errors.New("LIVE playlist requires --duration")
	}
	session := r.clock.Now()
	elapsed := 0.0

	if pl.InitURI != "" {
		opts := r.fetchOpts(initKind(j.segKind), cfg)
		opts.Stream = true
		if _, err := r.fetcher.Get(ctx, pl.InitURI, opts); err != nil {
			return err
		}
	}

	cursor := 0
	if mode == domain.ModeLive {
		// Apple recommends joining ~3 segments behind the live edge.
		cursor = len(pl.Segments) - 3
		if cursor < 0 {
			cursor = 0
		}
	}
	refresh := r.initialRefresh(cfg, pl)

	jitter := func(d time.Duration) time.Duration {
		f := 1 + (r.random.Float64()*2-1)*cfg.JitterFraction
		return time.Duration(float64(d) * f)
	}
	paceFactor := func() float64 {
		return 1 + (r.random.Float64()*2-1)*cfg.JitterFraction
	}
	sleepUntil := func(t time.Time) error {
		if d := t.Sub(r.clock.Now()); d > 0 {
			return r.sleeper.Sleep(ctx, d)
		}
		return nil
	}

	for {
		if mode == domain.ModeLive && r.clock.Since(session) >= cfg.PlayDuration {
			return nil
		}
		for i := cursor; i < len(pl.Segments); i++ {
			seg := pl.Segments[i]
			if mode == domain.ModeLive && r.clock.Since(session) >= cfg.PlayDuration {
				return nil
			}
			if err := sleepUntil(session.Add(time.Duration(elapsed * float64(time.Second)))); err != nil {
				return err
			}
			opts := r.fetchOpts(j.segKind, cfg)
			opts.Stream = true
			// Retries are exhausted inside the fetcher; a still-failed
			// segment is already recorded, playback continues.
			_, _ = r.fetcher.Get(ctx, seg.URI, opts)
			elapsed += seg.Duration * paceFactor()
			cursor = i + 1
		}
		switch mode {
		case domain.ModeVOD:
			return nil
		case domain.ModeEvent:
			if pl.EndList {
				return nil
			}
		case domain.ModeLive:
			if r.clock.Since(session) >= cfg.PlayDuration {
				return nil
			}
		}
		if err := r.sleeper.Sleep(ctx, jitter(refresh)); err != nil {
			return err
		}
		res, err := r.fetcher.Get(ctx, j.baseURL.String(), r.fetchOpts(j.plKind, cfg))
		if err != nil {
			return err
		}
		_, newPL, perr := r.parser.Parse(string(res.Body), j.baseURL)
		if perr != nil {
			return perr
		}
		if newPL == nil {
			return errors.New("media playlist turned into a master playlist during playback")
		}
		if newRefresh := time.Duration(newPL.TargetDuration * float64(time.Second)); cfg.LiveRefresh <= 0 && newRefresh > 0 {
			refresh = newRefresh
		}

		// Map the consumed position onto the (possibly moved) window.
		prevSeq := pl.MediaSequence + int64(cursor)
		pl = newPL
		switch {
		case prevSeq < pl.MediaSequence:
			cursor = 0 // the window slid past everything we consumed
		default:
			cursor = int(prevSeq - pl.MediaSequence)
			if cursor > len(pl.Segments) {
				cursor = len(pl.Segments)
			}
			if cursor < 0 {
				cursor = 0
			}
		}
	}
}

func (r *ClientRunner) initialRefresh(cfg Config, pl *domain.MediaPlaylist) time.Duration {
	if cfg.LiveRefresh > 0 {
		return cfg.LiveRefresh
	}
	if d := time.Duration(pl.TargetDuration * float64(time.Second)); d > 0 {
		return d
	}
	return time.Second
}
