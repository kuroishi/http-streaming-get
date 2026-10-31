package progress

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Source supplies the current progress snapshot. It must be safe to call
// from the display goroutine only.
type Source interface {
	Stats() Stats
}

// Display prints live one-line progress to a writer:
//
//   - rewrite=true  (a terminal): every tick rewrites the line with "\r";
//     Run closes the line with "\n" when its context is done.
//   - rewrite=false (redirected stderr): every tick appends one
//     newline-terminated snapshot line.
//
// The first paint happens immediately on Run, before the first tick.
type Display struct {
	out      io.Writer
	interval time.Duration
	rewrite  bool
}

// New builds a Display for the given interval. Callers pick interval and
// rewrite according to whether stderr is a terminal.
func New(out io.Writer, interval time.Duration, rewrite bool) *Display {
	if interval <= 0 {
		interval = time.Second
	}
	return &Display{out: out, interval: interval, rewrite: rewrite}
}

// Run paints snapshots until ctx is done.
func (d *Display) Run(ctx context.Context, src Source) {
	d.paint(src)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if d.rewrite {
				fmt.Fprint(d.out, "\n")
			}
			return
		case <-ticker.C:
			d.paint(src)
		}
	}
}

func (d *Display) paint(src Source) {
	line := Format(src.Stats())
	if d.rewrite {
		fmt.Fprintf(d.out, "\r%s", line)
	} else {
		fmt.Fprintln(d.out, line)
	}
}
