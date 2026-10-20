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
//
// Reactive flush: when `burstThreshold` > 0, Run also polls the buffer's
// pending-change counter every `burstPollInterval`. If the counter exceeds
// `burstThreshold` before the normal tick fires, an early flush is triggered.
// This keeps a deploy storm from collapsing into one delta without altering
// the normal cadence.
// Set burstThreshold = 0 to disable reactive flushing.
//
// Retention / GC: when `retainDays` > 0, a GC pass runs before every flush
// attempt. Running it before the write allows eligible old snapshots to free
// space even when the subsequent Put fails with ENOSPC. The anchor algorithm
// guarantees that every retained delta remains reconstructable.
type Snapshotter struct {
	buf               *Buffer
	store             storage.Store
	interval          time.Duration
	fullEvery         int
	burstThreshold    int           // flush early when pending changes >= this; 0 = disabled
	burstPollInterval time.Duration // how often to check the change counter
	retainDays        int           // delete snapshots older than this many days; 0 = keep forever
	now               func() time.Time

	mu         sync.Mutex
	prevState  delta.Snapshot
	prevID     types.SnapshotID
	flushNum   int
	flushFull  int // successful full-snapshot flushes since startup
	flushDelta int // successful delta flushes since startup

	healthMu            sync.RWMutex
	lastFlushAttempt    time.Time
	lastFlushSuccess    time.Time
	lastFlushErr        error
	consecutiveFailures int
}

// FlushHealth is a point-in-time view of snapshot persistence health.
type FlushHealth struct {
	LastAttempt         time.Time
	LastSuccess         time.Time
	LastError           error
	ConsecutiveFailures int
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
		buf:               buf,
		store:             store,
		interval:          interval,
		fullEvery:         fullEvery,
		burstThreshold:    50, // changes since the last flush; main.go overrides it
		burstPollInterval: 10 * time.Second,
		now:               time.Now,
	}
}

// WithBurstFlush configures reactive flushing. threshold is the number of
// pending changes that triggers an early flush; set to 0 to disable.
// pollInterval is how often the change counter is sampled.
func (s *Snapshotter) WithBurstFlush(threshold int, pollInterval time.Duration) *Snapshotter {
	s.burstThreshold = threshold
	s.burstPollInterval = pollInterval
	return s
}

// WithRetention sets the snapshot retention policy. Snapshots older than
// days days are eligible for deletion before each flush attempt.
// Setting days to 0 (the default) disables GC and keeps all snapshots.
func (s *Snapshotter) WithRetention(days int) *Snapshotter {
	if days < 0 {
		days = 0
	}
	s.retainDays = days
	return s
}

// WithClock injects a custom time source. Production code uses the
// default (time.Now); tests use this to produce deterministic IDs.
func (s *Snapshotter) WithClock(now func() time.Time) *Snapshotter {
	s.now = now
	return s
}

// Flush takes the current buffer state and persists it, returning the
// metadata of the snapshot written (full or delta). It is safe to call
// concurrently with itself. Run calls it on schedule; main.go calls it once
// more at shutdown.
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
		s.flushFull++
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
	s.flushDelta++
	return meta, nil
}

// FlushCounts returns the number of successful full and delta flushes since
// the agent started. Safe to call concurrently with Flush and Run.
func (s *Snapshotter) FlushCounts() (full, delta int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushFull, s.flushDelta
}

// FlushHealth returns the latest persistence outcome recorded by Run.
// Direct calls to Flush do not update this status because they do not represent
// the agent's scheduled recording loop.
func (s *Snapshotter) FlushHealth() FlushHealth {
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()
	return FlushHealth{
		LastAttempt:         s.lastFlushAttempt,
		LastSuccess:         s.lastFlushSuccess,
		LastError:           s.lastFlushErr,
		ConsecutiveFailures: s.consecutiveFailures,
	}
}

// FlushHealthy reports whether the scheduled recording loop has persisted at
// least one snapshot and has fewer than maxFailures consecutive failures.
func (s *Snapshotter) FlushHealthy(maxFailures int) bool {
	if maxFailures < 1 {
		maxFailures = 1
	}
	health := s.FlushHealth()
	return !health.LastSuccess.IsZero() && health.ConsecutiveFailures < maxFailures
}

func (s *Snapshotter) recordFlushOutcome(err error) {
	now := s.now()
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.lastFlushAttempt = now
	s.lastFlushErr = err
	if err != nil {
		s.consecutiveFailures++
		return
	}
	s.lastFlushSuccess = now
	s.consecutiveFailures = 0
}

// GC runs the retention pass immediately and blocks until it completes. It
// is the same pass Run makes before every flush attempt; tests call it
// directly. A no-op when retainDays is 0.
func (s *Snapshotter) GC(ctx context.Context) { s.gc(ctx) }

// gc runs the snapshot retention / garbage-collection algorithm.
// Run calls it before every flush attempt.
//
// Algorithm — anchor-based GC:
//
//  1. Compute the cutoff time: now - retainDays.
//  2. Walk the index in chronological order to find the "anchor" — the
//     most-recent full snapshot whose timestamp is at or before the cutoff.
//     This is the oldest full snapshot we must keep so that all deltas
//     in the retention window remain reconstructable.
//  3. Delete every snapshot strictly before the anchor (full and delta alike).
//     The anchor itself is preserved.
//
// If no anchor exists (i.e. the oldest full snapshot is still within the
// retention window) we do nothing — there is nothing safe to delete.
//
// gc acquires no internal lock of its own because it is always called from
// a context where the caller does NOT hold s.mu. Delete calls on the store
// are independent operations protected by the store's own lock.
func (s *Snapshotter) gc(ctx context.Context) {
	if s.retainDays <= 0 {
		return
	}

	cutoff := s.now().UTC().AddDate(0, 0, -s.retainDays)

	metas, err := s.store.List(ctx)
	if err != nil {
		slog.Error("snapshotter: gc: list snapshots", "err", err)
		return
	}
	// metas is sorted by Timestamp ascending (guaranteed by Local.List).

	// Find the anchor: the last full snapshot at or before cutoff.
	anchorIdx := -1
	for i, m := range metas {
		if m.Kind == types.KindFull && !m.Timestamp.After(cutoff) {
			anchorIdx = i
		}
	}
	if anchorIdx < 0 {
		// No full snapshot is old enough to serve as an anchor.
		// Nothing to delete.
		return
	}

	// Delete everything strictly before the anchor.
	deleted := 0
	for _, m := range metas[:anchorIdx] {
		if err := s.store.Delete(ctx, m.ID); err != nil {
			slog.Error("snapshotter: gc: delete snapshot", "id", m.ID, "err", err)
			// Continue — best-effort; leave the store consistent for the
			// index entries we did not touch.
			continue
		}
		deleted++
	}
	if deleted > 0 {
		slog.Info("snapshotter: gc: deleted old snapshots",
			"count", deleted,
			"cutoff", cutoff.Format("2006-01-02"),
			"retainDays", s.retainDays,
		)
	}
}

// Run starts the periodic flush loop. It blocks until ctx is cancelled
// and then returns ctx.Err(). Errors during individual flushes are
// logged but do not stop the loop — losing one snapshot is better than
// losing the agent.
//
// ready gates the first flush: Run waits for it to be closed before taking
// an immediate full snapshot and starting the ticker. That snapshot captures
// a complete cluster view rather than the partial buffer that exists
// before the informer caches finish their initial sync. Pass nil to
// skip the gate (used by tests that drive Flush directly). If ctx is
// cancelled while waiting, Run returns without ever flushing.
//
// Reactive flush: see the type comment. After a burst flush the periodic
// ticker is reset, so a tick does not follow it moments later.
func (s *Snapshotter) Run(ctx context.Context, ready <-chan struct{}) error {
	if ready != nil {
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	doFlush := func(reason string) {
		// Drained before the attempt, deliberately, even though a failed flush
		// then forgets the burst. The changes themselves are not lost: they
		// stay in the buffer and go out with the next successful flush.
		// Restoring the count on failure would instead re-trigger a burst
		// every poll interval for as long as storage keeps failing, turning
		// one ENOSPC into a retry loop; the periodic tick is the back-off.
		s.buf.DrainChanges()
		// GC before the write, so a full volume (ENOSPC) can free space for
		// the very flush that needs it.
		s.gc(ctx)
		if _, err := s.Flush(ctx); err != nil {
			s.recordFlushOutcome(err)
			slog.Error("snapshotter: flush failed", "err", err, "reason", reason)
			return
		}
		s.recordFlushOutcome(nil)
	}

	// Prove that storage is writable before readiness can become true. This
	// also removes the up-to-one-interval gap between informer sync and the
	// first persisted cluster state.
	doFlush("initial")

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// With burst flushing disabled burstC stays nil, and a nil channel never
	// fires in a select.
	var burstC <-chan time.Time
	if s.burstThreshold > 0 && s.burstPollInterval > 0 {
		burstTicker := time.NewTicker(s.burstPollInterval)
		defer burstTicker.Stop()
		burstC = burstTicker.C
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-ticker.C:
			doFlush("periodic")

		case <-burstC:
			if pending := s.buf.PendingChanges(); pending >= s.burstThreshold {
				slog.Info("snapshotter: burst threshold reached, flushing early",
					"pending", pending, "threshold", s.burstThreshold)
				doFlush("burst")
				// Restart the periodic window so a tick does not follow the
				// burst flush moments later.
				ticker.Reset(s.interval)
			}
		}
	}
}
