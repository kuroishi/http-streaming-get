// Package domain holds the pure HLS load-testing domain model: playlist
// structures, playback modes, the ports used by the use case layer, and pure
// selection policies. This package must not depend on any other package of
// this project, nor on I/O libraries beyond stdlib value types.
package domain

// Rendition is one variant stream entry of a master playlist
// (EXT-X-STREAM-INF).
type Rendition struct {
	Index         int    // position in the master playlist (0-based)
	Bandwidth     int    // peak bandwidth, bits per second
	AvgBandwidth  int    // average bandwidth, bits per second
	Resolution    string // e.g. "1920x1080"
	Codecs        string
	AudioGroup    string // EXT-X-STREAM-INF:AUDIO group id, if any
	SubtitleGroup string // EXT-X-STREAM-INF:SUBTITLES group id, if any
	URI           string // resolved absolute URI of the media playlist
}

// MediaTrack is an alternate rendition declared with EXT-X-MEDIA
// (audio or subtitle tracks).
type MediaTrack struct {
	Type     string // "AUDIO" or "SUBTITLES"
	GroupID  string
	Name     string
	Language string
	Default  bool   // DEFAULT=YES
	URI      string // resolved absolute URI of the media playlist, may be empty
}

// MasterPlaylist is a parsed master playlist.
type MasterPlaylist struct {
	Renditions []Rendition
	Media      []MediaTrack
}

// Segment is one media segment of a media playlist.
type Segment struct {
	URI      string  // resolved absolute URI
	Duration float64 // EXTINF duration in seconds
}

// MediaPlaylist is a parsed media playlist.
type MediaPlaylist struct {
	TargetDuration float64
	MediaSequence  int64
	PlaylistType   string // "", "VOD" or "EVENT"
	EndList        bool   // EXT-X-ENDLIST present
	InitURI        string // EXT-X-MAP init segment URI, if any
	Segments       []Segment
}

// PlaybackMode is the playback model a virtual client follows.
type PlaybackMode string

const (
	// ModeVOD: the playlist carries EXT-X-ENDLIST; play every listed
	// segment once, then stop.
	ModeVOD PlaybackMode = "vod"
	// ModeEvent: EXT-X-PLAYLIST-TYPE:EVENT without EXT-X-ENDLIST; play all
	// known segments, keep re-fetching for appended ones, stop after
	// EXT-X-ENDLIST appears and everything is consumed.
	ModeEvent PlaybackMode = "event"
	// ModeLive: a sliding window playlist; keep refreshing and consuming
	// until the requested play duration elapses.
	ModeLive PlaybackMode = "live"
)

// Mode classifies a media playlist into the VOD / EVENT / LIVE playback
// model.
func (p *MediaPlaylist) Mode() PlaybackMode {
	if p.EndList {
		return ModeVOD
	}
	if p.PlaylistType == "EVENT" {
		return ModeEvent
	}
	return ModeLive
}
