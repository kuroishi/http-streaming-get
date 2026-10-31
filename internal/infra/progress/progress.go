// Package progress renders the single-line live status shown while a run
// is in progress. It renders only; wiring lives in cmd/hsg.
package progress

import (
	"fmt"
	"time"
)

// Stats is one snapshot of run progress.
type Stats struct {
	Clients     int
	ClientsDone int
	Requests    int64
	Errors      int64
	Bytes       int64
	Elapsed     time.Duration
}

// Format renders one status line:
//
//	[+00:42] running | clients: 100 (63 done) | requests: 12500 | bytes: 2.2GB | 57.1MB/s | errors: 12
func Format(s Stats) string {
	secs := s.Elapsed.Seconds()
	throughput := 0.0
	if secs > 0 {
		throughput = float64(s.Bytes) / secs
	}
	return fmt.Sprintf("[+%02d:%02d] running | clients: %d (%d done) | requests: %d | bytes: %s | %s/s | errors: %d",
		int(s.Elapsed.Minutes()), int(s.Elapsed.Seconds())%60,
		s.Clients, s.ClientsDone, s.Requests, humanBytes(s.Bytes),
		humanDecimal(throughput), s.Errors)
}

// humanBytes is the 1024-based byte renderer (same style as the final
// summary on stdout).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// humanDecimal renders a bytes/second figure with decimal SI suffixes
// (MB = 1e6), which is conventional for bandwidth numbers.
func humanDecimal(v float64) string {
	const unit = 1000
	if v < unit {
		return fmt.Sprintf("%.1fB", v)
	}
	div, exp := float64(unit), 0
	for m := v / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", v/div, "KMGTPE"[exp])
}
