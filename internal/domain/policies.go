package domain

import (
	"fmt"
	"strings"
)

// SelectRendition picks the video rendition of a master playlist. A
// non-negative index selects deterministically by position; a negative index
// selects the highest-bandwidth rendition.
func SelectRendition(m *MasterPlaylist, index int) (*Rendition, error) {
	if m == nil || len(m.Renditions) == 0 {
		return nil, fmt.Errorf("master playlist has no renditions")
	}
	if index >= 0 {
		if index < len(m.Renditions) {
			return &m.Renditions[index], nil
		}
		return nil, fmt.Errorf("rendition %d out of range; available renditions:\n%s", index, ListRenditions(m))
	}
	best := 0
	for i := range m.Renditions {
		if m.Renditions[i].Bandwidth > m.Renditions[best].Bandwidth {
			best = i
		}
	}
	return &m.Renditions[best], nil
}

// ListRenditions renders the renditions of a master playlist as a
// human-readable table used in error messages.
func ListRenditions(m *MasterPlaylist) string {
	lines := make([]string, 0, len(m.Renditions))
	for _, r := range m.Renditions {
		res := r.Resolution
		if res == "" {
			res = "-"
		}
		lines = append(lines, fmt.Sprintf("  [%d] bandwidth=%d resolution=%s codecs=%s", r.Index, r.Bandwidth, res, r.Codecs))
	}
	return strings.Join(lines, "\n")
}

// SelectMediaTrack picks the audio or subtitle track a virtual client plays
// alongside the video rendition. Tracks are linked to a rendition through
// groups (EXT-X-STREAM-INF:AUDIO / :SUBTITLES). When lang is empty the
// DEFAULT=YES track of the rendition's group wins. ok is false when no
// track matches, in which case the client skips that kind of track — in
// particular, a rendition without a group link has its track muxed in and
// no separate track is fetched.
func SelectMediaTrack(m *MasterPlaylist, r *Rendition, trackType, lang string) (MediaTrack, bool) {
	if m == nil {
		return MediaTrack{}, false
	}
	group := ""
	if r != nil && strings.EqualFold(trackType, "AUDIO") {
		group = r.AudioGroup
	}
	if r != nil && strings.EqualFold(trackType, "SUBTITLES") {
		group = r.SubtitleGroup
	}
	if group == "" {
		return MediaTrack{}, false
	}
	var candidates []MediaTrack
	for _, t := range m.Media {
		if !strings.EqualFold(t.Type, trackType) {
			continue
		}
		if group != "" && t.GroupID != group {
			continue
		}
		candidates = append(candidates, t)
	}
	if lang != "" {
		for _, t := range candidates {
			if strings.EqualFold(t.Language, lang) || strings.EqualFold(t.Name, lang) {
				return t, true
			}
		}
		return MediaTrack{}, false
	}
	for _, t := range candidates {
		if t.Default {
			return t, true
		}
	}
	if len(candidates) > 0 {
		return candidates[0], true
	}
	return MediaTrack{}, false
}
