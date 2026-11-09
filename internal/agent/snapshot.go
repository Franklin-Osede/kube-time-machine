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

// Snapshotter periodically persists the Buffer's state to a storage.Store.
// Every fullEvery-th successful flush, starting with the first, is a full
// snapshot and the rest are deltas, so reconstruction needs at most one full
// snapshot plus fullEvery-1 deltas.
//
// When burst flushing is enabled, Run polls the buffer's pending-change count
// and flushes early once it reaches the threshold, so a deploy storm does not
// collapse into one delta. When retainDays > 0, a GC pass runs before every
// flush attempt; see gc.
type Snapshotter struct {
	buf               *Buffer
	store             storage.Store
	interval          time.Duration
	fullEvery         int
	burstThreshold    int
	burstPollInterval time.Duration
	retainDays        int
	now               func() time.Time

	mu         sync.Mutex
	prevState  delta.Snapshot
	prevID     types.SnapshotID
	flushNum   int
	flushFull  int
	flushDelta int

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

// NewSnapshotter constructs a Snapshotter. interval is how often Run
// flushes; fullEvery is how many flushes make one full-plus-deltas cycle
// (1 means every flush is full).
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
// pending changes that triggers an early flush; pollInterval is how often
// the counter is sampled. Either one at 0 disables it.
func (s *Snapshotter) WithBurstFlush(threshold int, pollInterval time.Duration) *Snapshotter {
	s.burstThreshold = threshold
	s.burstPollInterval = pollInterval
	return s
}

// WithRetention makes snapshots older than days eligible for deletion before
// each flush attempt. 0, the default, disables GC.
func (s *Snapshotter) WithRetention(days int) *Snapshotter {
	if days < 0 {
		days = 0
	}
	s.retainDays = days
	return s
}

// WithClock injects a custom time source, for deterministic IDs in tests.
func (s *Snapshotter) WithClock(now func() time.Time) *Snapshotter {
	s.now = now
	return s
}

// Flush persists the current buffer state as a full snapshot or a delta and
// returns its metadata. It is safe to call concurrently with itself.
func (s *Snapshotter) Flush(ctx context.Context) (types.SnapshotMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// The pending count is drained even if the write then fails. The changes
	// are not lost, because prevState is untouched on failure; restoring the
	// count would re-trigger a burst every poll while storage keeps failing.
	curr, _ := s.buf.SnapshotAndDrain()
	ts := s.now()

	isFull := s.flushNum%s.fullEvery == 0

	if isFull {
		meta, err := s.store.PutFull(ctx, ts, curr)
		if err != nil {
			return types.SnapshotMeta{}, fmt.Errorf("agent: put full: %w", err)
		}
		// Advance only after the write succeeds, or the next delta would be
		// anchored to a snapshot that was never persisted.
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

// FlushHealth returns the latest persistence outcome recorded by Run. Direct
// calls to Flush do not update it.
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

// GC runs the retention pass Run makes before every flush attempt. A no-op
// when retainDays is 0.
func (s *Snapshotter) GC(ctx context.Context) { s.gc(ctx) }

// gc deletes every snapshot older than the anchor: the newest full snapshot
// at or before now-retainDays. Keeping the anchor keeps every delta in the
// window reconstructable; with no anchor, nothing is safe to delete.
//
// gc takes no lock of its own; the store serialises each Delete.
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
	// Store.List promises Timestamp order, which the anchor search relies on.
	anchorIdx := -1
	for i, m := range metas {
		if m.Kind == types.KindFull && !m.Timestamp.After(cutoff) {
			anchorIdx = i
		}
	}
	if anchorIdx < 0 {
		return
	}

	deleted := 0
	for _, m := range metas[:anchorIdx] {
		if err := s.store.Delete(ctx, m.ID); err != nil {
			slog.Error("snapshotter: gc: delete snapshot", "id", m.ID, "err", err)
			continue // best-effort: Delete is crash-safe per snapshot
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

// Run flushes periodically until ctx is cancelled, then returns ctx.Err().
// A failed flush is logged and does not stop the loop.
//
// ready gates the first flush, so it is taken only after the informer caches
// have synced instead of from a partial buffer. nil skips the gate. If ctx is
// cancelled while waiting, Run returns without flushing.
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
		// GC first, so a full volume can free space for this flush.
		s.gc(ctx)
		if _, err := s.Flush(ctx); err != nil {
			s.recordFlushOutcome(err)
			slog.Error("snapshotter: flush failed", "err", err, "reason", reason)
			return
		}
		s.recordFlushOutcome(nil)
	}

	// Prove storage is writable before readiness can become true.
	doFlush("initial")

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// A nil burstC never fires in the select.
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
