package agent_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Franklin-Osede/kube-time-machine/internal/agent"
	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

type controlledFullStore struct {
	storage.Store
	mu   sync.Mutex
	fail bool
}

func (s *controlledFullStore) PutFull(ctx context.Context, ts time.Time, snap delta.Snapshot) (types.SnapshotMeta, error) {
	s.mu.Lock()
	fail := s.fail
	s.mu.Unlock()
	if fail {
		return types.SnapshotMeta{}, errBoom
	}
	return s.Store.PutFull(ctx, ts, snap)
}

func (s *controlledFullStore) setFail(fail bool) {
	s.mu.Lock()
	s.fail = fail
	s.mu.Unlock()
}

// newFSStore returns a real local filesystem Store rooted in t.TempDir().
// Snapshotter tests use the real storage to exercise the actual
// integration path rather than mocking.
func newFSStore(t *testing.T) storage.Store {
	t.Helper()
	s, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return s
}

// stepClock returns a closure that yields times advancing by `step`
// each call, starting at `start`. Lets tests produce deterministic IDs
// across multiple Flush calls.
func stepClock(start time.Time, step time.Duration) func() time.Time {
	t := start
	return func() time.Time {
		out := t
		t = t.Add(step)
		return out
	}
}

func at(year, month, day, hour, minute int) time.Time {
	return time.Date(year, time.Month(month), day, hour, minute, 0, 0, time.UTC)
}

func TestSnapshotter_FirstFlushIsFull(t *testing.T) {
	store := newFSStore(t)
	buf := agent.NewBuffer()
	buf.Upsert(k("Deployment", "default", "api"), delta.State("v1"))

	s := agent.NewSnapshotter(buf, store, time.Minute, 12).
		WithClock(stepClock(at(2026, 5, 18, 14, 0), time.Minute))

	meta, err := s.Flush(context.Background())
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if meta.Kind != types.KindFull {
		t.Errorf("first flush kind: want full, got %q", meta.Kind)
	}
	if meta.PrevID != "" {
		t.Errorf("first full snapshot should have empty PrevID, got %q", meta.PrevID)
	}
}

func TestSnapshotter_SubsequentFlushesAreDeltas(t *testing.T) {
	store := newFSStore(t)
	buf := agent.NewBuffer()
	s := agent.NewSnapshotter(buf, store, time.Minute, 12).
		WithClock(stepClock(at(2026, 5, 18, 14, 0), time.Minute))

	// Flush #0 — full snapshot of empty buffer.
	full, err := s.Flush(context.Background())
	if err != nil {
		t.Fatalf("Flush #0: %v", err)
	}

	// Mutate buffer, then Flush #1 — should be a delta against the full.
	buf.Upsert(k("Deployment", "default", "api"), delta.State("v1"))
	d1, err := s.Flush(context.Background())
	if err != nil {
		t.Fatalf("Flush #1: %v", err)
	}
	if d1.Kind != types.KindDelta {
		t.Errorf("Flush #1 kind: want delta, got %q", d1.Kind)
	}
	if d1.PrevID != full.ID {
		t.Errorf("Flush #1 PrevID: want %q, got %q", full.ID, d1.PrevID)
	}

	// Round-trip: loading the delta and applying it to the full's
	// snapshot must reproduce the buffer's current state.
	fullLoaded, err := store.Get(context.Background(), full.ID)
	if err != nil {
		t.Fatalf("Get full: %v", err)
	}
	deltaLoaded, err := store.Get(context.Background(), d1.ID)
	if err != nil {
		t.Fatalf("Get delta: %v", err)
	}
	reconstructed := delta.Apply(fullLoaded.Full, deltaLoaded.Delta)
	want := buf.Snapshot()
	if !reflect.DeepEqual(reconstructed, want) {
		t.Errorf("reconstruction failed\nwant: %v\ngot:  %v", want, reconstructed)
	}
}

// TestSnapshotter_FullEveryNFlushes verifies the cadence: with fullEvery=3,
// flushes 0 and 3 should be full, 1, 2, 4, 5 should be deltas.
func TestSnapshotter_FullEveryNFlushes(t *testing.T) {
	store := newFSStore(t)
	buf := agent.NewBuffer()
	s := agent.NewSnapshotter(buf, store, time.Minute, 3).
		WithClock(stepClock(at(2026, 5, 18, 14, 0), time.Minute))

	wantKinds := []types.SnapshotKind{
		types.KindFull,  // #0
		types.KindDelta, // #1
		types.KindDelta, // #2
		types.KindFull,  // #3
		types.KindDelta, // #4
		types.KindDelta, // #5
	}

	for i, want := range wantKinds {
		meta, err := s.Flush(context.Background())
		if err != nil {
			t.Fatalf("Flush #%d: %v", i, err)
		}
		if meta.Kind != want {
			t.Errorf("Flush #%d kind: want %q, got %q", i, want, meta.Kind)
		}
	}
}

// TestSnapshotter_FullEveryClampsToOne ensures a misconfigured
// fullEvery=0 doesn't divide-by-zero — we clamp to 1 (every flush full).
func TestSnapshotter_FullEveryClampsToOne(t *testing.T) {
	store := newFSStore(t)
	buf := agent.NewBuffer()
	s := agent.NewSnapshotter(buf, store, time.Minute, 0).
		WithClock(stepClock(at(2026, 5, 18, 14, 0), time.Minute))

	for i := 0; i < 3; i++ {
		meta, err := s.Flush(context.Background())
		if err != nil {
			t.Fatalf("Flush #%d: %v", i, err)
		}
		if meta.Kind != types.KindFull {
			t.Errorf("Flush #%d kind: want full (clamped), got %q", i, meta.Kind)
		}
	}
}

// failStore is a Store that returns a sentinel error on every Put.
// Used to verify Flush propagates storage errors.
type failStore struct{ storage.Store }

var errBoom = errors.New("boom")

func (failStore) PutFull(_ context.Context, _ time.Time, _ delta.Snapshot) (types.SnapshotMeta, error) {
	return types.SnapshotMeta{}, errBoom
}
func (failStore) PutDelta(_ context.Context, _ time.Time, _ types.SnapshotID, _ delta.Delta) (types.SnapshotMeta, error) {
	return types.SnapshotMeta{}, errBoom
}

func TestSnapshotter_FlushPropagatesStoreErrors(t *testing.T) {
	s := agent.NewSnapshotter(agent.NewBuffer(), failStore{}, time.Minute, 12).
		WithClock(stepClock(at(2026, 5, 18, 14, 0), time.Minute))

	_, err := s.Flush(context.Background())
	if !errors.Is(err, errBoom) {
		t.Errorf("expected wrapped errBoom, got %v", err)
	}
}

// flakyFullStore wraps a real Store and fails the first PutFull, then
// delegates. Used to prove a failed full write does not advance the
// Snapshotter's cadence (the C2 regression).
type flakyFullStore struct {
	storage.Store
	fullCalls int
}

func (f *flakyFullStore) PutFull(ctx context.Context, ts time.Time, snap delta.Snapshot) (types.SnapshotMeta, error) {
	f.fullCalls++
	if f.fullCalls == 1 {
		return types.SnapshotMeta{}, errBoom
	}
	return f.Store.PutFull(ctx, ts, snap)
}

// TestSnapshotter_FailedFullDoesNotAdvanceCadence pins the fix for the
// chain-corruption bug: if a full snapshot write fails, the next Flush
// must retry the SAME cadence slot (another full), not emit a delta
// anchored to a snapshot that was never persisted. Before the fix,
// flushNum advanced before the write, so the recovery flush was a delta
// with an empty PrevID — unreconstructable.
func TestSnapshotter_FailedFullDoesNotAdvanceCadence(t *testing.T) {
	store := &flakyFullStore{Store: newFSStore(t)}
	buf := agent.NewBuffer()
	buf.Upsert(k("Deployment", "default", "api"), delta.State("v1"))

	s := agent.NewSnapshotter(buf, store, time.Minute, 3).
		WithClock(stepClock(at(2026, 5, 18, 14, 0), time.Minute))

	// First flush attempts a full and fails.
	if _, err := s.Flush(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("first Flush: want errBoom, got %v", err)
	}

	// Recovery flush must still be a full (cadence not advanced).
	full, err := s.Flush(context.Background())
	if err != nil {
		t.Fatalf("recovery Flush: %v", err)
	}
	if full.Kind != types.KindFull {
		t.Fatalf("after a failed full, recovery kind: want full, got %q", full.Kind)
	}
	if full.PrevID != "" {
		t.Errorf("recovered full should have empty PrevID, got %q", full.PrevID)
	}

	// The following flush is a delta correctly anchored to the recovered full.
	buf.Upsert(k("Deployment", "default", "api2"), delta.State("v2"))
	d, err := s.Flush(context.Background())
	if err != nil {
		t.Fatalf("delta Flush: %v", err)
	}
	if d.Kind != types.KindDelta {
		t.Fatalf("kind after recovery: want delta, got %q", d.Kind)
	}
	if d.PrevID != full.ID {
		t.Errorf("delta PrevID: want recovered full %q, got %q", full.ID, d.PrevID)
	}
}

// TestSnapshotter_RunWaitsForReady pins the fix for the informer-sync
// race: Run must not take its first flush until the ready channel is
// closed, so the first (always-full) snapshot reflects a synced cluster
// rather than a partially-populated buffer.
func TestSnapshotter_RunWaitsForReady(t *testing.T) {
	store := newFSStore(t)
	buf := agent.NewBuffer()
	buf.Upsert(k("Deployment", "default", "api"), delta.State("v1"))

	s := agent.NewSnapshotter(buf, store, 20*time.Millisecond, 12)

	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	// Wait for Run to fully exit before the test returns. Otherwise its
	// flush loop can still be mid-write when t.TempDir cleanup runs
	// RemoveAll, racing on the storage dir ("directory not empty").
	done := make(chan struct{})
	go func() { _ = s.Run(ctx, ready); close(done) }()
	defer func() { cancel(); <-done }()

	// Several ticker intervals pass while ready is still open: no flush.
	time.Sleep(80 * time.Millisecond)
	metas, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 0 {
		t.Fatalf("Run flushed %d snapshot(s) before ready was closed", len(metas))
	}

	// Closing ready releases the loop; flushes begin.
	close(ready)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m, _ := store.List(context.Background()); len(m) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Run did not flush after ready was closed")
}

// TestSnapshotter_RunStopsOnContextCancel exercises the goroutine loop:
// a cancelled context must cause Run to return ctx.Err() promptly,
// without leaking the ticker goroutine.
func TestSnapshotter_RunStopsOnContextCancel(t *testing.T) {
	store := newFSStore(t)
	s := agent.NewSnapshotter(agent.NewBuffer(), store, 50*time.Millisecond, 12)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()

	// Let one or two ticks pass so we know the loop is alive.
	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of cancel")
	}
}

func TestSnapshotter_FlushHealthDegradesAndRecovers(t *testing.T) {
	realStore := newFSStore(t)
	store := &controlledFullStore{Store: realStore, fail: true}
	s := agent.NewSnapshotter(agent.NewBuffer(), store, 10*time.Millisecond, 1).
		WithBurstFlush(0, time.Hour)

	if !s.FlushHealthy(3) {
		t.Fatal("snapshotter should be healthy before the first scheduled attempt")
	}

	ready := make(chan struct{})
	close(ready)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ready) }()

	waitForFlushHealth(t, s, func(h agent.FlushHealth) bool {
		return h.ConsecutiveFailures >= 3
	})
	if s.FlushHealthy(3) {
		t.Fatal("snapshotter should be unhealthy after three consecutive failures")
	}
	failed := s.FlushHealth()
	if failed.LastError == nil || failed.LastAttempt.IsZero() || !failed.LastSuccess.IsZero() {
		t.Fatalf("unexpected failed health state: %+v", failed)
	}

	store.setFail(false)
	waitForFlushHealth(t, s, func(h agent.FlushHealth) bool {
		return h.ConsecutiveFailures == 0 && !h.LastSuccess.IsZero()
	})
	if !s.FlushHealthy(3) {
		t.Fatal("snapshotter should recover readiness after a successful flush")
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

func waitForFlushHealth(t *testing.T, s *agent.Snapshotter, ready func(agent.FlushHealth) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready(s.FlushHealth()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for flush health; current state: %+v", s.FlushHealth())
}

// putFullFailStore delegates all methods to an underlying Store but always
// returns errBoom from PutFull. Used to simulate a persistently full PVC
// (ENOSPC) while keeping List and Delete functional so GC can work.
type putFullFailStore struct{ storage.Store }

func (putFullFailStore) PutFull(_ context.Context, _ time.Time, _ delta.Snapshot) (types.SnapshotMeta, error) {
	return types.SnapshotMeta{}, errBoom
}

// TestSnapshotter_GCRunsBeforeFailedFullFlush pins the fix for R-03: when
// every full-flush attempt fails (simulating ENOSPC), GC must still run
// proactively so old snapshots are freed before the flush is retried.
// Without this, a full PVC deadlocks the agent forever.
func TestSnapshotter_GCRunsBeforeFailedFullFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	realStore := newFSStore(t)
	now := at(2026, 6, 1, 12, 0)

	// Seed directly into the real store (not through the Snapshotter) so the
	// timestamps are fully under our control.
	// - anchor at T-35d: oldest full snapshot inside the GC window;
	//   qualifies as anchor because T-35d ≤ cutoff(T-30d).
	// - old at T-60d: strictly before the anchor — GC must delete this.
	_, err := realStore.PutFull(ctx, now.AddDate(0, 0, -35), delta.Snapshot{})
	if err != nil {
		t.Fatalf("seed anchor: %v", err)
	}
	oldMeta, err := realStore.PutFull(ctx, now.AddDate(0, 0, -60), delta.Snapshot{})
	if err != nil {
		t.Fatalf("seed old: %v", err)
	}

	// Wrap so all PutFull calls fail; List and Delete pass through to realStore.
	failing := putFullFailStore{realStore}

	buf := agent.NewBuffer()
	ready := make(chan struct{})
	close(ready)

	s := agent.NewSnapshotter(buf, failing, 20*time.Millisecond, 1).
		WithRetention(30).
		WithClock(func() time.Time { return now })

	runDone := make(chan struct{})
	go func() { _ = s.Run(ctx, ready); close(runDone) }()

	// Allow several flush cycles so GC has time to run.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-runDone

	// Despite all flush failures, GC must have deleted the pre-anchor snapshot.
	ids := idsSet(mustList(t, realStore))
	if ids[oldMeta.ID] {
		t.Error("GC must delete old snapshots even when full flush always fails (R-03: ENOSPC deadlock)")
	}
}

// ---- GC / retention tests -------------------------------------------------
//
// All GC tests call s.GC() directly (the exported wrapper around the private
// gc()) so they don't need Run(). This also avoids timing sensitivity.

// gcStore returns a real filesystem Store and a Snapshotter pre-wired with
// a fixed-in-time clock so GC cutoffs are reproducible.
func gcStore(t *testing.T, retainDays int) (storage.Store, *agent.Snapshotter) {
	t.Helper()
	store := newFSStore(t)
	// fullEvery=1 so every Flush is a full snapshot → every call is an
	// eligible GC anchor candidate.
	s := agent.NewSnapshotter(agent.NewBuffer(), store, time.Minute, 1).
		WithRetention(retainDays)
	return store, s
}

// seedFull flushes a single full snapshot at the given absolute time and
// returns its metadata.
func seedFull(t *testing.T, s *agent.Snapshotter, ts time.Time) types.SnapshotMeta {
	t.Helper()
	once := true
	s.WithClock(func() time.Time {
		if once {
			once = false
			return ts
		}
		// Should not be called again during a single Flush, but be safe.
		return ts.Add(time.Millisecond)
	})
	meta, err := s.Flush(context.Background())
	if err != nil {
		t.Fatalf("seedFull at %s: %v", ts.Format(time.RFC3339), err)
	}
	return meta
}

// TestGC_NoOpWhenRetainDaysZero: GC disabled means nothing is ever deleted.
func TestGC_NoOpWhenRetainDaysZero(t *testing.T) {
	store, s := gcStore(t, 0)
	now := time.Now().UTC()

	m0 := seedFull(t, s, now.AddDate(0, 0, -60))
	m1 := seedFull(t, s, now.AddDate(0, 0, -40))

	// Fix the clock at "now" so gc sees today.
	s.WithClock(func() time.Time { return now })
	s.GC(context.Background())

	metas, _ := store.List(context.Background())
	ids := idsSet(metas)
	if !ids[m0.ID] || !ids[m1.ID] {
		t.Errorf("retainDays=0 should keep all snapshots; index: %v", metas)
	}
}

// TestGC_DeletesBeforeAnchor: the snapshot strictly before the anchor is
// deleted; the anchor and everything newer is preserved.
//
// Timeline (retainDays=30, "now" = T):
//
//	T-60d: full-0 → strictly before anchor → deleted
//	T-31d: full-1 → anchor (most-recent full ≤ T-30d) → kept
//	T-10d: full-2 → within retention window → kept
func TestGC_DeletesBeforeAnchor(t *testing.T) {
	store, s := gcStore(t, 30)
	now := time.Now().UTC()

	m0 := seedFull(t, s, now.AddDate(0, 0, -60)) // before anchor
	m1 := seedFull(t, s, now.AddDate(0, 0, -31)) // anchor
	m2 := seedFull(t, s, now.AddDate(0, 0, -10)) // in window

	s.WithClock(func() time.Time { return now })
	s.GC(context.Background())

	ids := idsSet(mustList(t, store))
	if ids[m0.ID] {
		t.Errorf("m0 (T-60d, before anchor) should be deleted")
	}
	if !ids[m1.ID] {
		t.Errorf("m1 (T-31d, anchor) should be kept")
	}
	if !ids[m2.ID] {
		t.Errorf("m2 (T-10d, in window) should be kept")
	}
}

// TestGC_NoAnchorKeepsEverything: when all full snapshots are within the
// retention window (none old enough to be an anchor), nothing is deleted.
func TestGC_NoAnchorKeepsEverything(t *testing.T) {
	store, s := gcStore(t, 30)
	now := time.Now().UTC()

	m0 := seedFull(t, s, now.AddDate(0, 0, -10))
	m1 := seedFull(t, s, now.AddDate(0, 0, -5))

	s.WithClock(func() time.Time { return now })
	s.GC(context.Background())

	ids := idsSet(mustList(t, store))
	for _, m := range []types.SnapshotMeta{m0, m1} {
		if !ids[m.ID] {
			t.Errorf("snapshot %s should be kept (no anchor)", m.ID)
		}
	}
}

// TestGC_SingleOldSnapshotIsAnchorNotDeleted: when only one full snapshot
// exists and it's older than the cutoff, it becomes the anchor and must
// not be deleted (there is nothing strictly before it).
func TestGC_SingleOldSnapshotIsAnchorNotDeleted(t *testing.T) {
	store, s := gcStore(t, 30)
	now := time.Now().UTC()

	m0 := seedFull(t, s, now.AddDate(0, 0, -60)) // sole anchor

	s.WithClock(func() time.Time { return now })
	s.GC(context.Background())

	ids := idsSet(mustList(t, store))
	if !ids[m0.ID] {
		t.Errorf("single old snapshot (anchor, nothing before it) must not be deleted")
	}
}

// TestGC_MultipleBatchesBeforeAnchor: several snapshots piled up before the
// anchor are all deleted in one GC pass.
func TestGC_MultipleBatchesBeforeAnchor(t *testing.T) {
	store, s := gcStore(t, 30)
	now := time.Now().UTC()

	// Three snapshots before the anchor.
	old0 := seedFull(t, s, now.AddDate(0, 0, -90))
	old1 := seedFull(t, s, now.AddDate(0, 0, -70))
	old2 := seedFull(t, s, now.AddDate(0, 0, -50))
	anchor := seedFull(t, s, now.AddDate(0, 0, -31))
	recent := seedFull(t, s, now.AddDate(0, 0, -5))

	s.WithClock(func() time.Time { return now })
	s.GC(context.Background())

	ids := idsSet(mustList(t, store))
	for _, m := range []types.SnapshotMeta{old0, old1, old2} {
		if ids[m.ID] {
			t.Errorf("snapshot %s (before anchor) should be deleted", m.ID)
		}
	}
	if !ids[anchor.ID] {
		t.Errorf("anchor snapshot should be kept")
	}
	if !ids[recent.ID] {
		t.Errorf("recent snapshot should be kept")
	}
}

// idsSet converts a slice of SnapshotMeta into a set of IDs for O(1) lookup.
func idsSet(metas []types.SnapshotMeta) map[types.SnapshotID]bool {
	out := make(map[types.SnapshotID]bool, len(metas))
	for _, m := range metas {
		out[m.ID] = true
	}
	return out
}

func mustList(t *testing.T, store storage.Store) []types.SnapshotMeta {
	t.Helper()
	metas, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return metas
}
