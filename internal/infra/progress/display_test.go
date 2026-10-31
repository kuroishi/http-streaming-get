package progress

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixedSource struct{ s Stats }

func (f fixedSource) Stats() Stats { return f.s }

// safeBuffer is a concurrent-safe fake progress stream.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunRewritesOnTerminal(t *testing.T) {
	buf := &safeBuffer{}
	d := New(buf, 50*time.Millisecond, true)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.Run(ctx, fixedSource{Stats{Clients: 2, Elapsed: time.Second}})
	}()
	time.Sleep(30 * time.Millisecond) // first paint
	cancel()
	wg.Wait()
	out := buf.String()
	if !strings.Contains(out, "\r") || strings.Count(out, "\n") != 1 {
		t.Fatalf("rewrite mode: want \\r updates and exactly one final \\n, got %q", out)
	}
}

func TestRunSnapshotModeForRedirectedStderr(t *testing.T) {
	buf := &safeBuffer{}
	d := New(buf, 20*time.Millisecond, false)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.Run(ctx, fixedSource{Stats{Clients: 2, Elapsed: time.Second}})
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	wg.Wait()
	out := buf.String()
	if strings.Contains(out, "\r") {
		t.Fatalf("snapshot mode must not emit \\r, got %q", out)
	}
	if n := strings.Count(out, "\n"); n < 1 {
		t.Fatalf("want at least one \\n-terminated snapshot line, got %d", n)
	}
}
