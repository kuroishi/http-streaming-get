// Command hsg-testserver serves a deterministic local HLS fixture for
// smoke-testing hsg building: a master playlist with two renditions and a
// default audio track, a VOD (with EXT-X-MAP), and a live playlist that
// appends one new segment on every refresh.
//
// Usage: hsg-testserver [-addr 127.0.0.1:8899]
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
)

const (
	listenAddr = "127.0.0.1:8899"
)

// server serves the fixture tree /master.m3u8, /vod/*/media.m3u8,
// /audio/en.m3u8 and /live/chunklist.m3u8; any known segment extension is
// mapped to deterministic fake bytes.
type server struct {
	mu       sync.Mutex
	liveSegs []string
}

func newServer() *server {
	return &server{liveSegs: []string{"l0.ts", "l1.ts", "l2.ts"}}
}

const masterText = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="/audio/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,AUDIO="aud"
/vod/720p/media.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1600000,RESOLUTION=1280x720,AUDIO="aud"
/vod/1080p/media.m3u8
`

func vodText() string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MAP:URI=\"init.m4s\"\n")
	for _, i := range []string{"0", "1", "2"} {
		fmt.Fprintf(&b, "#EXTINF:2.000,\nseg%s.m4s\n", i)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

const audioText = `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-PLAYLIST-TYPE:VOD
#EXTINF:2.000,
a0.m4s
#EXTINF:2.000,
a1.m4s
#EXT-X-ENDLIST
`

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch path {
	case "/master.m3u8":
		fmt.Fprint(w, masterText)
	case "/vod/720p/media.m3u8", "/vod/1080p/media.m3u8":
		fmt.Fprint(w, vodText())
	case "/audio/en.m3u8":
		fmt.Fprint(w, audioText)
	case "/live/chunklist.m3u8":
		fmt.Fprint(w, s.livePlaylist())
	default:
		if isSegment(path) {
			w.Write(fakeSegment(path))
			return
		}
		http.NotFound(w, r)
	}
}

// livePlaylist hands out the current window and appends one fresh segment
// per request, as a live origin would.
func (s *server) livePlaylist() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n")
	for _, seg := range s.liveSegs {
		fmt.Fprintf(&b, "#EXTINF:2.000,\n%s\n", seg)
	}
	next := fmt.Sprintf("l%d.ts", len(s.liveSegs))
	s.liveSegs = append(s.liveSegs, next)
	return b.String()
}

func isSegment(path string) bool {
	for _, ext := range []string{".m4s", ".ts", ".mp4"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

func fakeSegment(path string) []byte {
	n := 4096
	if strings.Contains(path, "audio") || strings.Contains(path, "init") {
		n = 2048
	}
	return bytes.Repeat([]byte{byte(len(path))}, n)
}

func main() {
	addr := flag.String("addr", listenAddr, "listen address")
	flag.Parse()
	http.Handle("/", newServer())
	log.Printf("hsg-testserver serving on http://%s/ (GET /master.m3u8)", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
