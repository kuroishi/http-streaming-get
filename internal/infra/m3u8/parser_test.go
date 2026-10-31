package m3u8

import (
	"net/url"
	"strings"
	"testing"

	"github.com/kuroishi/hsg/internal/domain"
)

const masterText = `#EXTM3U
#EXT-X-VERSION:6

#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="audio/en.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Japanese",LANGUAGE="ja",URI="audio/ja.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="subs/en.vtt.m3u8"

#EXT-X-STREAM-INF:BANDWIDTH=2000000,AVERAGE-BANDWIDTH=1900000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud"
720p/media.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud"
1080p/media.m3u8
`

func mustBase(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestParserMaster(t *testing.T) {
	base := mustBase(t, "https://example.com/vod/master.m3u8")
	master, media, err := New().Parse(masterText, base)
	if err != nil {
		t.Fatal(err)
	}
	if media != nil {
		t.Fatal("master text must not yield a media playlist")
	}
	if len(master.Renditions) != 2 {
		t.Fatalf("renditions = %d, want 2", len(master.Renditions))
	}
	r0 := master.Renditions[0]
	if r0.Index != 0 || r0.Bandwidth != 2000000 || r0.AvgBandwidth != 1900000 {
		t.Fatalf("rendition[0] wrong: %+v", r0)
	}
	if r0.Resolution != "1280x720" {
		t.Fatalf("resolution = %q", r0.Resolution)
	}
	// CODECS is a quoted comma list; the quote-aware splitter must keep it intact
	if r0.Codecs != "avc1.64001f,mp4a.40.2" {
		t.Fatalf("codecs = %q", r0.Codecs)
	}
	if r0.AudioGroup != "aud" {
		t.Fatalf("audio group = %q", r0.AudioGroup)
	}
	// Relative URIs resolve against the master playlist URL.
	if r0.URI != "https://example.com/vod/720p/media.m3u8" {
		t.Fatalf("rendition uri = %q", r0.URI)
	}
	if master.Renditions[1].URI != "https://example.com/vod/1080p/media.m3u8" {
		t.Fatalf("rendition[1] uri = %q", master.Renditions[1].URI)
	}
	if len(master.Media) != 3 {
		t.Fatalf("media tracks = %d, want 3", len(master.Media))
	}
	audio := master.Media[0]
	if audio.Type != "AUDIO" || !audio.Default || audio.GroupID != "aud" {
		t.Fatalf("audio track wrong: %+v", audio)
	}
	if audio.URI != "https://example.com/vod/audio/en.m3u8" {
		t.Fatalf("audio uri = %q", audio.URI)
	}
	subs := master.Media[2]
	if subs.Type != "SUBTITLES" || !subs.Default {
		t.Fatalf("subtitle track wrong: %+v", subs)
	}
}

const vodMediaText = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.006,
seg-0.m4s
#EXTINF:5.500,title
seg-1.m4s
#EXT-X-ENDLIST
`

func TestParserMediaPlaylist(t *testing.T) {
	base := mustBase(t, "https://example.com/vod/1080p/media.m3u8")
	master, pl, err := New().Parse(vodMediaText, base)
	if err != nil {
		t.Fatal(err)
	}
	if master != nil {
		t.Fatal("media text must not yield a master playlist")
	}
	if pl.TargetDuration != 6 || pl.MediaSequence != 10 || pl.PlaylistType != "VOD" || !pl.EndList {
		t.Fatalf("playlist metadata wrong: %+v", pl)
	}
	if pl.InitURI != "https://example.com/vod/1080p/init.mp4" {
		t.Fatalf("init uri = %q", pl.InitURI)
	}
	if len(pl.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(pl.Segments))
	}
	if pl.Segments[0].Duration != 6.006 || pl.Segments[0].URI != "https://example.com/vod/1080p/seg-0.m4s" {
		t.Fatalf("segment[0] wrong: %+v", pl.Segments[0])
	}
	if pl.Segments[1].Duration != 5.5 {
		t.Fatalf("segment[1] duration = %v", pl.Segments[1].Duration)
	}
	if pl.Mode() != domain.ModeVOD {
		t.Fatalf("mode = %q", pl.Mode())
	}
}

const liveMediaText = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:2680
#EXTINF:4.0,
l0.ts
#EXTINF:4.0,
l1.ts
#EXTINF:4.0,
l2.ts
`

func TestParserLiveMediaPlaylist(t *testing.T) {
	base := mustBase(t, "https://example.com/live/chunklist.m3u8")
	_, pl, err := New().Parse(liveMediaText, base)
	if err != nil {
		t.Fatal(err)
	}
	if pl.EndList || pl.PlaylistType != "" {
		t.Fatalf("expected a plain LIVE playlist, got %+v", pl)
	}
	if pl.Mode() != domain.ModeLive {
		t.Fatalf("mode = %q", pl.Mode())
	}
	if pl.MediaSequence != 2680 || len(pl.Segments) != 3 {
		t.Fatalf("playlist wrong: %+v", pl)
	}
	// Segments resolve against the playlist's own directory.
	if pl.Segments[0].URI != "https://example.com/live/l0.ts" {
		t.Fatalf("segment uri = %q", pl.Segments[0].URI)
	}
}

func TestParserErrors(t *testing.T) {
	if _, _, err := New().Parse("?EXTM3U?\n", mustBase(t, "https://x/y.m3u8")); err == nil {
		t.Fatal("want error for input without STREAM-INF or segments")
	}
	p := New()
	if _, _, err := p.Parse(masterText, nil); err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatalf("want base URL error, got %v", err)
	}
	bad := "#EXTM3U\n#EXTINF:abc,\nseg0.ts\n"
	if _, _, err := p.Parse(bad, mustBase(t, "https://x/l.m3u8")); err == nil {
		t.Fatal("want error for bad EXTINF duration")
	}
	orphan := "#EXTM3U\nseg0.ts\n"
	if _, _, err := p.Parse(orphan, mustBase(t, "https://x/l.m3u8")); err == nil || !strings.Contains(err.Error(), "without a preceding EXTINF") {
		t.Fatalf("want orphan segment error, got %v", err)
	}
}

func TestParseAttrsQuoteAware(t *testing.T) {
	attrs := parseAttrs(`BANDWIDTH=1000,CODECS="a,b,c",RESOLUTION="640x36 0",NOTE="x,y")
`)
	if attrs["BANDWIDTH"] != "1000" {
		t.Fatalf("BANDWIDTH = %q", attrs["BANDWIDTH"])
	}
	if attrs["CODECS"] != "a,b,c" {
		t.Fatalf("CODECS = %q", attrs["CODECS"])
	}
}
