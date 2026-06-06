package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// Snapshotter periodically takes the current state from a Buffer and
// persists it to a storage.Store, alternating between full reference
// snapshots and incremental deltas.
//
// Cadence:
//   - Every `interval`, Flush is called.
//   - Flush #0 and every Flush whose count is a multiple of `fullEvery`
//     produce a full reference snapshot.
//   - All other flushes produce a delta against the most recent
//     persisted snapshot.
//
// This bounds reconstruction cost: replaying any historical state needs
// at most one full snapshot + `fullEvery-1` deltas.
type Snapshotter struct {
	buf       *Buffer
	store     storage.Store
	interval  time.Duration
	fullEvery int
	now       func() time.Time

	mu        sync.Mutex
	prevState delta.Snapshot
	prevID    types.SnapshotID
	flushNum  int
}

// NewSnapshotter constructs a Snapshotter with the given cadence policy.
// `interval` is how often Run flushes; `fullEvery` is how many flushes
// pass between full reference snapshots (a value of 1 means every flush
// is a full snapshot; a value of 12 yields 1 full + 11 deltas per cycle).
func NewSnapshotter(buf *Buffer, store storage.Store, interval time.Duration, fullEvery int) *Snapshotter {
	if fullEvery < 1 {
		fullEvery = 1
	}
	return &Snapshotter{
		buf:       buf,
		store:     store,
		interval:  interval,
		fullEvery: fullEvery,
		now:       time.Now,
	}
}

// WithClock injects a custom time source. Production code uses the
// default (time.Now); tests use this to produce deterministic IDs.
func (s *Snapshotter) WithClock(now func() time.Time) *Snapshotter {
	s.now = now
	return s
}

// Flush takes the current buffer state and persists it. It returns the
// metadata of the snapshot that was written (full or delta). Flush is
// safe to call concurrently with itself; the internal lock serializes
// access to prevState / prevID / flushNum.
//
// Flush is exposed (not just used internally by Run) so the eventual CLI
// can implement a "force flush now" command on top.
func (s *Snapshotter) Flush(ctx context.Context) (types.SnapshotMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	curr := s.buf.Snapshot()
	ts := s.now()

	isFull := s.flushNum%s.fullEvery == 0

	if isFull {
		meta, err := s.store.PutFull(ctx, ts, curr)
		if err != nil {
			return types.SnapshotMeta{}, fmt.Errorf("agent: put full: %w", err)
		}
		// Advance internal state ONLY after the write succeeds. If the
		// write failed and we had already incremented flushNum, the next
		// Flush would emit a delta anchored to a snapshot that was never
		// persisted (prevID still "" or stale) — an unreconstructable
		// chain. Keeping the position pinned means a failed flush simply
		// retries the same cadence slot (here: another full) next tick.
		s.prevState = curr
		s.prevID = meta.ID
		s.flushNum++
		return meta, nil
	}

	d := delta.Compute(s.prevState, curr)
	meta, err := s.store.PutDelta(ctx, ts, s.prevID, d)
	if err != nil {
		return types.SnapshotMeta{}, fmt.Errorf("agent: put delta: %w", err)
	}
	s.prevState = curr
	s.prevID = meta.ID
	s.flushNum++
	return meta, nil
}

// Run starts the periodic flush loop. It blocks until ctx is cancelled
// and then returns ctx.Err(). Errors during individual flushes are
// logged but do not stop the loop — losing one snapshot is better than
// losing the agent.
//
// ready gates the first flush: Run waits for it to be closed before
// starting the ticker, so the first (always-full) snapshot captures a
// complete cluster view rather than the partial buffer that exists
// before the informer caches finish their initial sync. Pass nil to
// skip the gate (used by tests that drive Flush directly). If ctx is
// cancelled while waiting, Run returns without ever flushing.
func (s *Snapshotter) Run(ctx context.Context, ready <-chan struct{}) error {
	if ready != nil {
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := s.Flush(ctx); err != nil {
				slog.Error("snapshotter: flush failed", "err", err)
			}
		}
	}
}
