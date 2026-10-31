# HTTP Streaming Get (hsg)

`hsg` is a load-testing CLI for HLS (HTTP Live Streaming) servers. It spawns many
virtual clients that fetch a master playlist, pick a rendition, and download media
segments (plus audio and subtitle tracks) at real-time playback pace — then reports
latency, throughput, and error metrics.

```
$ hsg --clients 100 --duration 2m https://cdn.example.com/live/master.m3u8
```

## Install

```
go install github.com/kuroishi/hsg@latest
```

## Usage

```
Usage: hsg [flags] <master-or-media-playlist-url>
```

Flags must appear before the URL.

| Flag | Default | Description |
|------|---------|-------------|
| `--clients` | `1` | Number of virtual clients |
| `--rendition` | `-1` | Rendition index in the master playlist (0-based); default picks the highest bandwidth |
| `--audio-lang` | *(DEFAULT=YES)* | Audio track language or NAME to select |
| `--subtitle-lang` | *(DEFAULT=YES)* | Subtitle track language or NAME to select |
| `--duration` | — | Playback duration; **required for LIVE playlists** |
| `--live-refresh` | playlist `TARGETDURATION` | LIVE playlist refresh interval |
| `--retries` | `3` | Retries per request (exponential backoff); 429/5xx and network errors are retried |
| `--timeout` | off | Overall run timeout |
| `--json` | off | Print the summary as JSON |
| `--http1` | off | Force HTTP/1.1 (per-client keep-alive connection pool) |
| `--seed` | `0` | Random seed for reproducible jitter (0 = time-based) |
| `-q`, `--quiet` | off | Suppress the live progress line on stderr (the final summary is still printed) |

### Modes

A virtual client selects its mode from the playlist itself:

- **VOD** — the playlist has `EXT-X-ENDLIST`: play every listed segment once, then exit.
- **EVENT** — `EXT-X-PLAYLIST-TYPE:EVENT` without `EXT-X-ENDLIST`: play all known
  segments, keep re-fetching the playlist for new ones, exit when `EXT-X-ENDLIST`
  appears and everything is consumed.
- **LIVE** — a sliding window (no `EXT-X-PLAYLIST-TYPE:EVENT`, no `EXT-X-ENDLIST`):
  join at three segments behind the live edge, keep refreshing the playlist and
  consuming new segments until `--duration` elapses (required; hsg errors out
  without it).

### Client behavior

- Master playlist → one video rendition (measured playback close to a real player).
- Associated audio and subtitle renditions (`EXT-X-MEDIA`) are played in parallel
  by the same client: the `DEFAULT=YES` track by default, or the track matching
  `--audio-lang` / `--subtitle-lang`. Missing tracks are skipped.
- Segments are fetched sequentially, paced by the real segment duration with ±10%
  jitter (startup timing is jittered too, so clients don't sync perfectly).
- `EXT-X-MAP` (fMP4 init segment) is fetched once per playlist when present.
- Failed requests are retried (default 3×) with exponential backoff; retries are
  recorded. Playlist fetch failures end the client; failed segments are skipped
  and playback continues.
- Interrupting with `Ctrl-C` prints the metrics collected so far.

While a run is in progress hsg prints a live one-line status (elapsed time,
active/done clients, request/byte/error counters) to stderr — rewritten in
place on terminals, snapshot lines every 10s when stderr is redirected.
Pass `--quiet` to silence it.

### Metrics

Aggregated per request kind (`playlist`, `playlist-audio`, `playlist-subtitle`,
`segment`, `segment-audio`, `segment-subtitle`, `init`, `init-audio`,
`init-subtitle`): request count, errors, bytes, TTFB and duration percentiles
(p50/p95/p99), plus total throughput.

```
wall: 2m0.1s | clients: 100 | requests: 12640 | errors: 32 | bytes: 2.4GB | throughput: 20.6MB/s

kind                     reqs  errors         bytes |    ttfb p50/p95/p99 ms |      dur p50/p95/p99 ms
playlist                 100       0         1.2MB |            12/28/45   |            14/31/48
segment                12355      30         2.1GB |            18/64/210  |            52/190/604
```

With `--json` the same summary is emitted as a JSON object (pipe into `jq`).

## Assumptions and non-goals

- hsg measures byte delivery. It does **not** decode media, and `EXT-X-KEY`
  (DRM / AES-128) tags are ignored — encrypted streams are downloaded as-is.
- I-frame playlists, `EXT-X-GAP`, and `EXT-X-BYTERANGE` segments are not
  interpreted (segments are downloaded as plain URIs).
- Byte counts are post-decompression (the Go HTTP transport transparently
  decompresses gzip responses).
- Adaptive bitrate emulation is future work; one run uses one rendition.

## Roadmap

- ABR-like rendition switching driven by measured bandwidth
- Ramp-up / stepped load profiles
- Per-client rendition distribution
- HTML/graph report output

## Development

```
go build ./...
go vet ./...
go test ./...
go run ./cmd/hsg --help
```

The code follows a clean-architecture layering: `internal/domain` (pure
model, ports, selection policies) ← `internal/usecase` (the virtual client
state machine) ← `internal/infrastructure` adapters (`internal/infra`: m3u8
parser, HTTP fetcher, metrics, clock/rng) wired together in `cmd/hsg`.
Dependencies point inward only; all unit tests run against fakes, so the
suite is fast and deterministic.

Design notes/implementation plan are in `plan-2026-10-31.md` (Japanese).
