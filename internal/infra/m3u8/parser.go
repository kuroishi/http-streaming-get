// Package m3u8 implements the domain.PlaylistParser port as an M3U8 text
// parser.
package m3u8

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/kuroishi/hsg/internal/domain"
)

const maxLineSize = 1 << 20

// Parser parses M3U8 playlists.
type Parser struct{}

// New returns a Parser.
func New() *Parser { return &Parser{} }

// Parse implements domain.PlaylistParser: master playlists become
// *domain.MasterPlaylist, media playlists become *domain.MediaPlaylist.
func (p *Parser) Parse(text string, base *url.URL) (*domain.MasterPlaylist, *domain.MediaPlaylist, error) {
	if base == nil {
		return nil, nil, errors.New("m3u8: base URL is required")
	}
	if strings.Contains(text, "#EXT-X-STREAM-INF") {
		m, err := parseMaster(text, base)
		return m, nil, err
	}
	pl, err := parseMedia(text, base)
	return nil, pl, err
}

func parseMaster(text string, base *url.URL) (*domain.MasterPlaylist, error) {
	var m domain.MasterPlaylist
	var pending *domain.Rendition
	sc := newScanner(text)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			if pending != nil {
				pending.URI = resolveURL(base, line)
				pending.Index = len(m.Renditions)
				m.Renditions = append(m.Renditions, *pending)
				pending = nil
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			pending = parseStreamInf(line[len("#EXT-X-STREAM-INF:"):])
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			if t := parseMediaTag(line[len("#EXT-X-MEDIA:"):], base); t.Type != "" {
				m.Media = append(m.Media, t)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("m3u8: scan failed: %w", err)
	}
	if len(m.Renditions) == 0 {
		return nil, errors.New("m3u8: no EXT-X-STREAM-INF entries found")
	}
	return &m, nil
}

func parseMedia(text string, base *url.URL) (*domain.MediaPlaylist, error) {
	pl := &domain.MediaPlaylist{}
	pendingDuration := -1.0
	sc := newScanner(text)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			tag, rest, ok := cutTag(line)
			if !ok {
				continue
			}
			switch tag {
			case "EXTINF":
				if pendingDuration >= 0 {
					return nil, errors.New("m3u8: EXTINF without a following segment URI")
				}
				d, _, _ := strings.Cut(rest, ",")
				f, err := strconv.ParseFloat(strings.TrimSpace(d), 64)
				if err != nil {
					return nil, fmt.Errorf("m3u8: bad EXTINF duration %q: %w", d, err)
				}
				pendingDuration = f
			case "EXT-X-ENDLIST":
				pl.EndList = true
			case "EXT-X-TARGETDURATION":
				f, err := strconv.ParseFloat(rest, 64)
				if err != nil {
					return nil, fmt.Errorf("m3u8: bad EXT-X-TARGETDURATION %q: %w", rest, err)
				}
				pl.TargetDuration = f
			case "EXT-X-MEDIA-SEQUENCE":
				n, err := strconv.ParseInt(rest, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("m3u8: bad EXT-X-MEDIA-SEQUENCE %q: %w", rest, err)
				}
				pl.MediaSequence = n
			case "EXT-X-PLAYLIST-TYPE":
				pl.PlaylistType = strings.ToUpper(strings.TrimSpace(rest))
			case "EXT-X-MAP":
				attrs := parseAttrs(rest)
				if u := attrs["URI"]; u != "" {
					pl.InitURI = resolveURL(base, u)
				}
			}
			continue
		}
		if pendingDuration < 0 {
			return nil, fmt.Errorf("m3u8: segment URI %q without a preceding EXTINF", line)
		}
		pl.Segments = append(pl.Segments, domain.Segment{
			URI:      resolveURL(base, line),
			Duration: pendingDuration,
		})
		pendingDuration = -1.0
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("m3u8: scan failed: %w", err)
	}
	if len(pl.Segments) == 0 {
		return nil, errors.New("m3u8: media playlist has no segments")
	}
	return pl, nil
}

// cutTag splits "#TAG:rest" into "TAG" and "rest"; plain comments like
// "# comment" yield ok=false.
func cutTag(line string) (tag, rest string, ok bool) {
	body := strings.TrimPrefix(line, "#")
	if !strings.Contains(body, ":") {
		return body, "", true
	}
	tag, rest, _ = strings.Cut(body, ":")
	return tag, rest, true
}

// parseAttrs parses comma-separated KEY=VALUE attribute pairs while
// respecting double-quoted values (e.g. CODECS lists contain commas).
func parseAttrs(s string) map[string]string {
	attrs := make(map[string]string)
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if v := strings.TrimSpace(cur.String()); v != "" {
		parts = append(parts, v)
	}
	for _, part := range parts {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		val = strings.Trim(val, "\"")
		attrs[key] = val
	}
	return attrs
}

func parseStreamInf(s string) *domain.Rendition {
	attrs := parseAttrs(s)
	bw, _ := strconv.Atoi(attrs["BANDWIDTH"])
	abw, _ := strconv.Atoi(attrs["AVERAGE-BANDWIDTH"])
	return &domain.Rendition{
		Bandwidth:     bw,
		AvgBandwidth:  abw,
		Resolution:    attrs["RESOLUTION"],
		Codecs:        attrs["CODECS"],
		AudioGroup:    attrs["AUDIO"],
		SubtitleGroup: attrs["SUBTITLES"],
	}
}

func parseMediaTag(s string, base *url.URL) domain.MediaTrack {
	attrs := parseAttrs(s)
	t := domain.MediaTrack{
		Type:     strings.ToUpper(attrs["TYPE"]),
		GroupID:  attrs["GROUP-ID"],
		Name:     attrs["NAME"],
		Language: attrs["LANGUAGE"],
		Default:  strings.EqualFold(attrs["DEFAULT"], "YES"),
	}
	if u := attrs["URI"]; u != "" {
		t.URI = resolveURL(base, u)
	}
	return t
}

func resolveURL(base *url.URL, ref string) string {
	u, err := base.Parse(ref)
	if err != nil {
		return ref
	}
	return u.String()
}

func newScanner(text string) *bufio.Scanner {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	return sc
}
