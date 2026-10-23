package engine

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/amwolff/jellybot/internal/fsop"
	"github.com/amwolff/jellybot/internal/store"
)

// OpDone reports the outcome of one operation.
type OpDone struct {
	Rel     string
	Sidecar bool
	Err     error
}

// ApplyProgress reports the bytes of all operations.
type ApplyProgress struct {
	Done, Total int64
	// Current is the source path of the current operation.
	Current string
}

// Run applies ops in order with a. When a main file fails, Run skips its
// sidecars. It records the new paths in s (which can be nil). It calls done
// after each operation, and progress at most ten times each second.
func Run(ctx context.Context, a *fsop.Applier, s *store.Store, ops []Op, progress func(ApplyProgress), done func(OpDone)) {
	var total int64
	for _, op := range ops {
		total += op.Size
	}

	var base int64
	var last time.Time
	report := func(cur Op, done, opTotal int64) {
		if now := time.Now(); now.Sub(last) >= 100*time.Millisecond || done == opTotal {
			last = now
			n := base
			if opTotal > 0 {
				n += cur.Size * done / opTotal
			}
			progress(ApplyProgress{Done: n, Total: total, Current: cur.Src})
		}
	}

	failed := make(map[string]bool)
	for _, op := range ops {
		if ctx.Err() != nil {
			return
		}
		if op.Sidecar && failed[op.Rel] {
			base += op.Size
			continue
		}

		applier := *a
		applier.Progress = func(done, total int64) { report(op, done, total) }
		err := applier.Apply(ctx, op.Src, op.Dst)
		if err == nil && s != nil {
			err = recordApplied(s, op.Src, op.Dst)
		}
		if err != nil && !op.Sidecar {
			failed[op.Rel] = true
		}
		if err != nil && op.Sidecar {
			err = fmt.Errorf("sidecar %s: %w", op.Dst, err)
		}
		base += op.Size
		done(OpDone{Rel: op.Rel, Sidecar: op.Sidecar, Err: err})
	}
}

// recordApplied copies the hash memo of src to dst, so that the next scan of
// the library does not read dst again.
func recordApplied(s *store.Store, src, dst string) error {
	e, ok, err := s.Stat(src)
	if err != nil || !ok {
		return err
	}
	fi, err := os.Stat(dst)
	if err != nil {
		return err
	}
	if fi.Size() != e.Size {
		return nil
	}
	e.MtimeNs = fi.ModTime().UnixNano()
	return s.PutStat(dst, e)
}

// Applied records the outcome of an operation in the plan.
func (p *Plan) Applied(d OpDone) {
	r := p.index[d.Rel]
	if r == nil {
		return
	}
	switch {
	case !d.Sidecar && d.Err == nil:
		r.Applied, r.ApplyErr = true, nil
	case !d.Sidecar:
		r.ApplyErr = d.Err
	case d.Err != nil:
		r.ApplyErr = d.Err
	}
}
