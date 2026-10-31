package usecase_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kuroishi/hsg/internal/domain"
	"github.com/kuroishi/hsg/internal/infra/m3u8"
	"github.com/kuroishi/hsg/internal/usecase"
)

// --- fakes (outer layer may instantiate real parser) ---

// fakeTime doubles as domain.Clock and domain.Sleeper: sleeps advance the
// virtual clock so pacing tests run instantly.
type fakeTime struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newFakeTime() *fakeTime {
	return &fakeTime{now: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)}
}

func (f *fakeTime) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeTime) Since(t time.Time) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now.Sub(t)
}

func (f *fakeTime) Sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.sleeps = append(f.sleeps, d)
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (f *fakeTime) sleepTotal() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := time.Duration(0)
	for _, d := range f.sleeps {
		total += d
	}
	return total
}

// fixedRandom neutralizes jitter (pace factor 1.0) while still producing
// the startup-jitter sleep of 0.5s.
type fixedRandom struct{ v float64 }

func (r fixedRandom) Float64() float64 { return r.v }

// callLog records (kind, url) fetches in order.
type callLog struct {
	mu      sync.Mutex
	entries []string
}

func (c *callLog) add(kind, url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, kind+" "+url)
}

func (c *callLog) count(url string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.entries {
		if strings.HasSuffix(e, " "+url) {
			n++
		}
	}
	return n
}

func (c *callLog) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// fakeFetcher serves prepared responses per URL; responseFunc is invoked per
// call so tests can evolve a playlist (LIVE/EVENT) or inject failures.
type fakeFetcher struct {
	mu        sync.Mutex
	log       *callLog
	responses map[string]func(call int) (string, error)
	calls     map[string]int
}

func newFakeFetcher(log *callLog) *fakeFetcher {
	return &fakeFetcher{log: log, responses: map[string]func(int) (string, error){}, calls: map[string]int{}}
}

func (f *fakeFetcher) serve(url string, fn func(call int) (string, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[url] = fn
}

func (f *fakeFetcher) serveStatic(url, body string) {
	f.serve(url, func(int) (string, error) { return body, nil })
}

func (f *fakeFetcher) serveFailing(url string) {
	f.serve(url, func(int) (string, error) { return "", fmt.Errorf("fetch failed for %s", url) })
}

func (f *fakeFetcher) Get(ctx context.Context, rawURL string, opts domain.GetOptions) (*domain.FetchResult, error) {
	f.mu.Lock()
	n := f.calls[rawURL] + 1
	f.calls[rawURL] = n
	fn, ok := f.responses[rawURL]
	f.mu.Unlock()
	f.log.add(opts.Kind, rawURL)
	if !ok {
		return nil, fmt.Errorf("unexpected fetch of %q", rawURL)
	}
	body, err := fn(n)
	return &domain.FetchResult{Body: []byte(body)}, err
}

func (f *fakeFetcher) callsOf(url string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[url]
}

// --- helpers ---

func playlistText(target int, dur float64, typ string, endList bool, uris ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n", target)
	if typ != "" {
		fmt.Fprintf(&b, "#EXT-X-PLAYLIST-TYPE:%s\n", typ)
	}
	for _, u := range uris {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", dur, u)
	}
	if endList {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.String()
}

const masterText = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="audio/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,AUDIO="aud"
720p/media.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,AUDIO="aud"
1080p/media.m3u8
`

func newRunner(fetcher domain.Fetcher, ft *fakeTime, rnd domain.Random) *usecase.ClientRunner {
	return usecase.NewClientRunner(fetcher, m3u8.New(), ft, ft, rnd)
}

// --- tests ---

func TestVODPlaybackFetchesInitAndPacesSegments(t *testing.T) {
	ft := newFakeTime()
	log := &callLog{}
	f := newFakeFetcher(log)

	masterURL := "https://cdn.test/vod/master.m3u8"
	f.serveStatic(masterURL, masterText)
	// Automatic rendition selection must pick 1080p (highest bandwidth).
	f.serveStatic("https://cdn.test/vod/1080p/media.m3u8",
		playlistText(2, 2, "VOD", true, "seg0.m4s", "seg1.m4s", "seg2.m4s"))
	f.serveStatic("https://cdn.test/vod/audio/en.m3u8",
		playlistText(2, 2, "VOD", true, "a0.m4s", "a1.m4s", "a2.m4s", "a3.m4s"))
	for i := 0; i < 3; i++ {
		f.serveStatic(fmt.Sprintf("https://cdn.test/vod/1080p/seg%d.m4s", i), "video-bytes")
	}
	for i := 0; i < 4; i++ {
		f.serveStatic(fmt.Sprintf("https://cdn.test/vod/audio/a%d.m4s", i), "audio-bytes")
	}

	err := newRunner(f, ft, fixedRandom{v: 0.5}).Run(context.Background(), usecase.Config{
		URL:            masterURL,
		RenditionIndex: -1, // automatic: highest bandwidth
		Retry:          domain.RetryPolicy{},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	// Highest-bandwidth rendition was chosen; 720p is never touched.
	for _, e := range log.entries {
		if strings.Contains(e, "720p") {
			t.Fatalf("720p should not be fetched: %v", log.entries)
		}
	}
	for i := 0; i < 3; i++ {
		if log.count(fmt.Sprintf("https://cdn.test/vod/1080p/seg%d.m4s", i)) != 1 {
			t.Fatalf("video seg%d fetched wrong number of times", i)
		}
	}
	for i := 0; i < 4; i++ {
		if log.count(fmt.Sprintf("https://cdn.test/vod/audio/a%d.m4s", i)) != 1 {
			t.Fatalf("audio a%d fetched wrong number of times", i)
		}
	}
	for _, k := range []string{domain.KindPlaylist, domain.KindPlaylistAudio} {
		found := false
		for _, e := range log.entries {
			if strings.HasPrefix(e, k+" ") {
				found = true
			}
		}
		if !found {
			t.Fatalf("kind %q was never fetched", k)
		}
	}

	// Real-time pacing: both tracks must sleep while segment fetches are
	// instant. The fake clock is shared between the two, concurrent track
	// goroutines, so only a bounded range is assertable here; exact pacing
	// is verified in TestVODDirectMediaPlaylistInitializesPacing.
	min := 500*time.Millisecond + 2*time.Second
	max := 500*time.Millisecond + 2*time.Second + 2*time.Second + 6*time.Second
	if total := ft.sleepTotal(); total <= min || total > max {
		t.Fatalf("total sleep = %v, want in (%v, %v]", total, min, max)
	}
}

func TestVODDirectMediaPlaylistWithInit(t *testing.T) {
	ft := newFakeTime()
	log := &callLog{}
	f := newFakeFetcher(log)

	mediaURL := "https://cdn.test/vod/media.m3u8"
	f.serveStatic(mediaURL, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:4.0,\ns0.m4s\n#EXTINF:4.0,\ns1.m4s\n#EXT-X-ENDLIST\n")
	for _, u := range []string{"https://cdn.test/vod/init.mp4", "https://cdn.test/vod/s0.m4s", "https://cdn.test/vod/s1.m4s"} {
		f.serveStatic(u, "bytes")
	}
	if err := newRunner(f, ft, fixedRandom{v: 0.5}).Run(context.Background(), usecase.Config{URL: mediaURL}); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	// The init segment is fetched once, before any segment.
	if log.count("https://cdn.test/vod/init.mp4") != 1 {
		t.Fatal("init segment must be fetched exactly once")
	}
	firstSegment := strings.Index(strings.Join(log.entries, "\n"), "segment https://")
	firstInit := strings.Index(strings.Join(log.entries, "\n"), "init https://")
	if firstInit < 0 || firstSegment < 0 || firstInit > firstSegment {
		t.Fatalf("init must be fetched before the segments: %v", log.entries)
	}
}

func TestVODDirectMediaPlaylistInitializesPacing(t *testing.T) {
	ft := newFakeTime()
	log := &callLog{}
	f := newFakeFetcher(log)

	mediaURL := "https://cdn.test/vod/media.m3u8"
	f.serveStatic(mediaURL, playlistText(4, 4, "VOD", true, "s0.m4s", "s1.m4s", "s2.m4s"))
	for _, u := range []string{
		"https://cdn.test/vod/s0.m4s", "https://cdn.test/vod/s1.m4s", "https://cdn.test/vod/s2.m4s",
	} {
		f.serveStatic(u, "bytes")
	}
	if err := newRunner(f, ft, fixedRandom{v: 0.5}).Run(context.Background(), usecase.Config{URL: mediaURL}); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	// A single track must pace exactly like a real player: start after the
	// jittered startup delay, then one 4s gap before each next segment
	// (segment fetches are instant in the fake).
	ft.mu.Lock()
	sleeps := append([]time.Duration(nil), ft.sleeps...)
	ft.mu.Unlock()
	if len(sleeps) != 3 {
		t.Fatalf("sleeps = %v, want 3 entries", sleeps)
	}
	for i, want := range []time.Duration{500 * time.Millisecond, 4 * time.Second, 4 * time.Second} {
		if sleeps[i] != want {
			t.Fatalf("sleep[%d] = %v, want %v", i, sleeps[i], want)
		}
	}
}

func TestVODContinuesAfterPermanentSegmentFailure(t *testing.T) {
	ft := newFakeTime()
	log := &callLog{}
	f := newFakeFetcher(log)

	mediaURL := "https://cdn.test/media.m3u8"
	f.serveStatic(mediaURL, playlistText(4, 2, "VOD", true, "s0.m4s", "s1.m4s", "s2.m4s"))
	f.serveStatic("https://cdn.test/s0.m4s", "ok")
	f.serveFailing("https://cdn.test/s1.m4s")
	f.serveStatic("https://cdn.test/s2.m4s", "ok")

	err := newRunner(f, ft, fixedRandom{v: 0.5}).Run(context.Background(), usecase.Config{URL: mediaURL})
	if err != nil {
		t.Fatalf("a failed segment must not abort the session: %v", err)
	}
	if log.count("https://cdn.test/s2.m4s") != 1 {
		t.Fatal("playback must continue after a failed segment")
	}
	if got := log.count("https://cdn.test/s1.m4s"); got != 1 {
		t.Fatalf("failed segment attempts = %d, want 1 (retries are the fetcher's job)", got)
	}
}

func TestLIVEPlaybackRefreshesUntilDurationElapses(t *testing.T) {
	ft := newFakeTime()
	log := &callLog{}
	f := newFakeFetcher(log)

	liveURL := "https://cdn.test/live/chunklist.m3u8"
	var segs []string
	seed := []string{"l0.ts", "l1.ts", "l2.ts"}
	segs = append(segs, seed...)
	var mu sync.Mutex

	f.serve(liveURL, func(int) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		dur := 2.0
		text := playlistText(2, dur, "", false, segs...)
		// Every refresh appends one new segment (as a live origin would).
		segs = append(segs, fmt.Sprintf("l%d.ts", len(segs)))
		return text, nil
	})
	// Segments resolve against https://cdn.test/live/.
	mu.Lock()
	existing := append([]string(nil), segs...)
	mu.Unlock()
	for _, s := range existing {
		f.serveStatic("https://cdn.test/live/"+s, "bytes")
	}

	err := newRunner(f, ft, fixedRandom{v: 0.5}).Run(context.Background(), usecase.Config{
		URL:          liveURL,
		PlayDuration: 8 * time.Second,
	})
	if err != nil {
		t.Fatalf("live run failed: %v", err)
	}
	if ft := f.callsOf(liveURL); ft < 2 {
		t.Fatalf("live playlist refreshed %d times, want >= 2", ft)
	}
	// New segments appended by later refreshes must have been consumed.
	consumedNew := false
	for _, e := range log.entries {
		if strings.Contains(e, "l3.ts") || strings.Contains(e, "l4.ts") {
			consumedNew = true
		}
	}
	if !consumedNew {
		t.Fatalf("newly appended segments must be consumed: %v", log.entries)
	}
}

func TestLIVEWithoutDurationFails(t *testing.T) {
	ft := newFakeTime()
	log := &callLog{}
	f := newFakeFetcher(log)

	liveURL := "https://cdn.test/live/chunklist.m3u8"
	f.serveStatic(liveURL, playlistText(2, 2, "", false, "l0.ts", "l1.ts", "l2.ts"))
	for _, s := range []string{"l0.ts", "l1.ts", "l2.ts"} {
		f.serveStatic("https://cdn.test/live/"+s, "bytes")
	}

	err := newRunner(f, ft, fixedRandom{v: 0.5}).Run(context.Background(), usecase.Config{URL: liveURL})
	if err == nil || !strings.Contains(err.Error(), "--duration") {
		t.Fatalf("want --duration error for a LIVE playlist, got %v", err)
	}
}

func TestEVENTPlaybackEndsOnEndlist(t *testing.T) {
	ft := newFakeTime()
	log := &callLog{}
	f := newFakeFetcher(log)

	eventURL := "https://cdn.test/event/media.m3u8"
	// call 1: two segments, still running; call 2: adds one; call 3: ENDLIST.
	var mu sync.Mutex
	f.serve(eventURL, func(call int) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		switch call {
		case 1:
			return playlistText(2, 2, "EVENT", false, "e0.m4s", "e1.m4s"), nil
		case 2:
			return playlistText(2, 2, "EVENT", false, "e0.m4s", "e1.m4s", "e2.m4s"), nil
		default:
			return playlistText(2, 2, "EVENT", true, "e0.m4s", "e1.m4s", "e2.m4s"), nil
		}
	})
	for _, s := range []string{"e0.m4s", "e1.m4s", "e2.m4s"} {
		f.serveStatic("https://cdn.test/event/"+s, "bytes")
	}

	err := newRunner(f, ft, fixedRandom{v: 0.5}).Run(context.Background(), usecase.Config{URL: eventURL})
	if err != nil {
		t.Fatalf("event run failed: %v", err)
	}
	if f.callsOf(eventURL) != 3 {
		t.Fatalf("playlist fetched %d times, want 3 (initial + 2 refreshes)", f.callsOf(eventURL))
	}
	for i, s := range []string{"e0.m4s", "e1.m4s", "e2.m4s"} {
		if log.count(fmt.Sprintf("https://cdn.test/event/%s", s)) != 1 {
			t.Fatalf("event segment %d not consumed exactly once", i)
		}
	}
}

func TestBadPlaylistURLFails(t *testing.T) {
	ft := newFakeTime()
	log := &callLog{}
	f := newFakeFetcher(log)
	err := newRunner(f, ft, fixedRandom{v: 0.5}).Run(context.Background(), usecase.Config{URL: "ht tp://bad"})
	if err == nil {
		t.Fatal("want an error for an unparseable URL")
	}
}
