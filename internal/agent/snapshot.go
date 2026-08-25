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
// This preserves intra-interval granularity during deploy storms (e.g. 20
// resources changed in 30 seconds) without altering the normal cadence.
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
		burstThreshold:    50, // flush early after 50 changes in one poll window
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

// GC runs the snapshot retention/GC algorithm immediately and blocks until
// it completes. It is identical to the gc pass that Run triggers after each
// full flush, but exposed so callers (tests, a future "ktm gc" CLI command)
// can invoke it on demand. Returns nil when retainDays==0 (GC disabled).
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
// Reactive flush: when burstThreshold > 0, Run also polls buf.PendingChanges()
// every burstPollInterval. If the counter reaches the threshold before the
// normal tick, an early flush is triggered and the tick timer is reset, so
// we don't double-flush within the same window.
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
		s.buf.DrainChanges() // reset counter before flush so the next window starts clean
		// Run GC before every flush attempt. This ensures old snapshots are
		// freed even when the subsequent full write fails (e.g. ENOSPC), which
		// would otherwise deadlock the agent because GC only ran post-success.
		// GC is a no-op when nothing is old enough to delete.
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

	// burstTicker drives the burst-detection poll. When burst flushing is
	// disabled (threshold == 0) we still create the ticker but never act on
	// it — the overhead is negligible and it simplifies the select.
	burstInterval := s.burstPollInterval
	if burstInterval <= 0 {
		burstInterval = s.interval // fall back to normal interval, effectively a no-op
	}
	burstTicker := time.NewTicker(burstInterval)
	defer burstTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-ticker.C:
			doFlush("periodic")
			// Reset the burst ticker so a burst flush immediately after a
			// periodic flush doesn't fire again within burstPollInterval.
			burstTicker.Reset(burstInterval)

		case <-burstTicker.C:
			if s.burstThreshold > 0 && s.buf.PendingChanges() >= s.burstThreshold {
				slog.Info("snapshotter: burst threshold reached, flushing early",
					"pending", s.buf.PendingChanges(), "threshold", s.burstThreshold)
				doFlush("burst")
				// Reset the periodic ticker so we don't double-flush within
				// the same normal interval.
				ticker.Reset(s.interval)
			}
		}
	}
}
