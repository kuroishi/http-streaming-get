package progress

import (
	"strings"
	"testing"
	"time"
)

func TestFormatLine(t *testing.T) {
	s := Stats{
		Clients: 100, ClientsDone: 63,
		Requests: 12500, Errors: 12,
		Bytes: 2_400_000_000, Elapsed: 42 * time.Second,
	}
	got := Format(s)
	want := "[+00:42] running | clients: 100 (63 done) | requests: 12500 | bytes: 2.2GB | 57.1MB/s | errors: 12"
	if got != want {
		t.Fatalf("Format:\n got  %q\n want %q", got, want)
	}
}

func TestFormatDoesNotWrapElapsedMinutes(t *testing.T) {
	s := Stats{Clients: 2, Requests: 5, Elapsed: 65*time.Minute + 10*time.Second}
	if got := Format(s); !strings.HasPrefix(got, "[+65:10]") {
		t.Fatalf("elapsed minutes must not wrap at 60: got %q", got)
	}
}

func TestFormatAtStartupNoDivisionByZero(t *testing.T) {
	if got := Format(Stats{Clients: 4, Elapsed: 0}); !strings.Contains(got, "0B/s") {
		t.Fatalf("zero elapsed must not panic and must show 0B/s: %q", got)
	}
}
