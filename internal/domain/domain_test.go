package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMediaPlaylistMode(t *testing.T) {
	cases := []struct {
		name string
		pl   MediaPlaylist
		want PlaybackMode
	}{
		{"endlist wins over playlist type", MediaPlaylist{PlaylistType: "EVENT", EndList: true}, ModeVOD},
		{"plain endlist", MediaPlaylist{EndList: true}, ModeVOD},
		{"event", MediaPlaylist{PlaylistType: "EVENT"}, ModeEvent},
		{"live by default", MediaPlaylist{TargetDuration: 6}, ModeLive},
		{"vod playlist type still live until endlist", MediaPlaylist{PlaylistType: "VOD"}, ModeLive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pl.Mode(); got != tc.want {
				t.Fatalf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}

func sampleMaster() *MasterPlaylist {
	return &MasterPlaylist{
		Renditions: []Rendition{
			{Index: 0, Bandwidth: 2000000, Resolution: "1280x720", AudioGroup: "aud"},
			{Index: 1, Bandwidth: 5000000, Resolution: "1920x1080", AudioGroup: "aud"},
			{Index: 2, Bandwidth: 5000000, Resolution: "3840x2160"},
		},
		Media: []MediaTrack{
			{Type: "AUDIO", GroupID: "aud", Name: "English", Language: "en", Default: true, URI: "https://x/a-en.m3u8"},
			{Type: "AUDIO", GroupID: "aud", Name: "Japanese", Language: "ja", URI: "https://x/a-ja.m3u8"},
			{Type: "SUBTITLES", GroupID: "subs", Name: "English", Language: "en", Default: true, URI: "https://x/s-en.m3u8"},
		},
	}
}

func TestSelectRenditionAutoPicksHighestBandwidth(t *testing.T) {
	r, err := SelectRendition(sampleMaster(), -1)
	if err != nil {
		t.Fatal(err)
	}
	// Renditions 1 and 2 share the top bandwidth; the first of them wins.
	if r.Index != 1 {
		t.Fatalf("rendition index = %d, want 1", r.Index)
	}
}

func TestSelectRenditionByIndex(t *testing.T) {
	r, err := SelectRendition(sampleMaster(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Bandwidth != 2000000 {
		t.Fatalf("bandwidth = %d, want 2000000", r.Bandwidth)
	}
}

func TestSelectRenditionOutOfRangeListsOptions(t *testing.T) {
	_, err := SelectRendition(sampleMaster(), 5)
	if err == nil {
		t.Fatal("want error for out-of-range rendition")
	}
	for _, want := range []string{"rendition 5 out of range", "[0]", "[1]", "[2]"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestSelectRenditionEmpty(t *testing.T) {
	_, err := SelectRendition(&MasterPlaylist{}, -1)
	if err == nil {
		t.Fatal("want error for empty master playlist")
	}
}

func TestSelectMediaTrackGroupAndDefault(t *testing.T) {
	m := sampleMaster()
	r := &m.Renditions[0] // 720p, AUDIO group "aud"
	track, ok := SelectMediaTrack(m, r, "AUDIO", "")
	if !ok {
		t.Fatal("want audio track")
	}
	if track.URI != "https://x/a-en.m3u8" {
		t.Fatalf("track uri = %q, want DEFAULT=YES english track", track.URI)
	}
	// Rendition 2 has no audio group: its audio is muxed in, so no
	// separate audio track is fetched.
	r2 := &m.Renditions[2]
	if _, ok := SelectMediaTrack(m, r2, "AUDIO", ""); ok {
		t.Fatal("no separate audio track must be selected for a rendition without an AUDIO group")
	}
}

func TestSelectMediaTrackLangOverride(t *testing.T) {
	m := sampleMaster()
	r := &m.Renditions[0]
	track, ok := SelectMediaTrack(m, r, "AUDIO", "ja")
	if !ok {
		t.Fatal("want japanese audio track")
	}
	if track.Language != "ja" {
		t.Fatalf("language = %q, want ja", track.Language)
	}
	// Match by NAME as well.
	track, ok = SelectMediaTrack(m, r, "AUDIO", "english")
	if !ok || track.URI != "https://x/a-en.m3u8" {
		t.Fatalf("want english by name, got %+v ok=%v", track, ok)
	}
	// Unknown language: skip the track entirely.
	if _, ok := SelectMediaTrack(m, r, "AUDIO", "fr"); ok {
		t.Fatal("want no track for unknown language")
	}
}

func TestSelectMediaTrackNoSubtitles(t *testing.T) {
	m := sampleMaster()
	r := &m.Renditions[0] // verRendition without SUBTITLES group link
	if _, ok := SelectMediaTrack(m, r, "SUBTITLES", ""); ok {
		t.Fatal("want no subtitle track when rendition has no SUBTITLES group")
	}
}

func TestSelectMediaTrackNoGroupMeansMuxed(t *testing.T) {
	m := sampleMaster()
	r := &m.Renditions[2] // no audio group link: audio is muxed
	if _, ok := SelectMediaTrack(m, r, "AUDIO", ""); ok {
		t.Fatal("rendition without a group link must not fetch a separate track")
	}
}

func TestRetryPolicyBackoff(t *testing.T) {
	p := RetryPolicy{MaxRetries: 3, Backoff: 500 * time.Millisecond}
	if p.Attempts() != 4 {
		t.Fatalf("attempts = %d, want 4", p.Attempts())
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}
	for attempt, w := range want {
		if got := p.BackoffFor(attempt + 1); got != w {
			t.Fatalf("backoff(%d) = %v, want %v", attempt+1, got, w)
		}
	}
	// Zero / negative backoff falls back to 500ms; huge shifts are capped.
	q := RetryPolicy{MaxRetries: 40}
	if q.BackoffFor(1) != 500*time.Millisecond {
		t.Fatalf("default base backoff wrong: %v", q.BackoffFor(1))
	}
	if q.BackoffFor(100) != 500*time.Millisecond<<16 {
		t.Fatalf("capped backoff wrong: %v", q.BackoffFor(100))
	}
}

func TestRequestObservationCarriesError(t *testing.T) {
	o := RequestObservation{Kind: KindSegment, URL: "https://x/s", Err: errors.New("boom")}
	if o.Err == nil {
		t.Fatal("error must survive the observation struct")
	}
}
