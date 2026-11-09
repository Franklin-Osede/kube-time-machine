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
func newFSStore(t *testing.T) storage.Store {
	t.Helper()
	s, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return s
}

// stepClock returns a clock that starts at start and advances by step per
// call, for deterministic IDs.
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

	full, err := s.Flush(context.Background())
	if err != nil {
		t.Fatalf("Flush #0: %v", err)
	}

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

	// Applying the delta to the full must reproduce the buffer.
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

// TestSnapshotter_FullEveryNFlushes: with fullEvery=3, flushes 0 and 3 are
// full and the rest are deltas.
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

// TestSnapshotter_FullEveryClampsToOne: fullEvery=0 is clamped to 1.
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

// failStore is a Store that returns errBoom on every Put.
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

// flakyFullStore fails the first PutFull, then delegates.
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

// TestSnapshotter_FailedFullDoesNotAdvanceCadence: after a failed full
// write the next Flush is another full, not a delta anchored to a snapshot
// that was never persisted.
func TestSnapshotter_FailedFullDoesNotAdvanceCadence(t *testing.T) {
	store := &flakyFullStore{Store: newFSStore(t)}
	buf := agent.NewBuffer()
	buf.Upsert(k("Deployment", "default", "api"), delta.State("v1"))

	s := agent.NewSnapshotter(buf, store, time.Minute, 3).
		WithClock(stepClock(at(2026, 5, 18, 14, 0), time.Minute))

	if _, err := s.Flush(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("first Flush: want errBoom, got %v", err)
	}

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

// TestSnapshotter_RunWaitsForReady: Run takes no flush until ready is
// closed, so the first full snapshot reflects a synced cluster.
func TestSnapshotter_RunWaitsForReady(t *testing.T) {
	store := newFSStore(t)
	buf := agent.NewBuffer()
	buf.Upsert(k("Deployment", "default", "api"), delta.State("v1"))

	s := agent.NewSnapshotter(buf, store, 20*time.Millisecond, 12)

	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	// Wait for Run to exit, or a mid-write flush races t.TempDir cleanup.
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

	close(ready)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m, _ := store.List(context.Background()); len(m) > 0 {
			if !s.FlushHealthy(3) {
				t.Fatal("snapshotter should be healthy after its initial flush")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Run did not flush after ready was closed")
}

// TestSnapshotter_RunStopsOnContextCancel: Run returns ctx.Err() promptly
// on cancellation.
func TestSnapshotter_RunStopsOnContextCancel(t *testing.T) {
	store := newFSStore(t)
	s := agent.NewSnapshotter(agent.NewBuffer(), store, 50*time.Millisecond, 12)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()

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

	if s.FlushHealthy(3) {
		t.Fatal("snapshotter should not be ready before its first successful scheduled flush")
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

// putFullFailStore always fails PutFull, like a full PVC, while List and
// Delete still work.
type putFullFailStore struct{ storage.Store }

func (putFullFailStore) PutFull(_ context.Context, _ time.Time, _ delta.Snapshot) (types.SnapshotMeta, error) {
	return types.SnapshotMeta{}, errBoom
}

// TestSnapshotter_GCRunsBeforeFailedFullFlush: GC runs even when every flush
// fails, so a full volume can free space.
func TestSnapshotter_GCRunsBeforeFailedFullFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	realStore := newFSStore(t)
	now := at(2026, 6, 1, 12, 0)

	// T-35d is the anchor (newest full at or before the T-30d cutoff); T-60d
	// is before it and must be deleted.
	_, err := realStore.PutFull(ctx, now.AddDate(0, 0, -35), delta.Snapshot{})
	if err != nil {
		t.Fatalf("seed anchor: %v", err)
	}
	oldMeta, err := realStore.PutFull(ctx, now.AddDate(0, 0, -60), delta.Snapshot{})
	if err != nil {
		t.Fatalf("seed old: %v", err)
	}

	failing := putFullFailStore{realStore}

	buf := agent.NewBuffer()
	ready := make(chan struct{})
	close(ready)

	s := agent.NewSnapshotter(buf, failing, 20*time.Millisecond, 1).
		WithRetention(30).
		WithClock(func() time.Time { return now })

	runDone := make(chan struct{})
	go func() { _ = s.Run(ctx, ready); close(runDone) }()

	time.Sleep(150 * time.Millisecond)
	cancel()
	<-runDone

	ids := idsSet(mustList(t, realStore))
	if ids[oldMeta.ID] {
		t.Error("GC must delete old snapshots even when full flush always fails (ENOSPC deadlock)")
	}
}

// GC tests call s.GC() directly, so they do not depend on Run's timing.

// gcStore returns a filesystem Store and a Snapshotter with the given
// retention.
func gcStore(t *testing.T, retainDays int) (storage.Store, *agent.Snapshotter) {
	t.Helper()
	store := newFSStore(t)
	// fullEvery=1: every Flush is full, so every snapshot can be an anchor.
	s := agent.NewSnapshotter(agent.NewBuffer(), store, time.Minute, 1).
		WithRetention(retainDays)
	return store, s
}

// seedFull flushes a full snapshot at ts.
func seedFull(t *testing.T, s *agent.Snapshotter, ts time.Time) types.SnapshotMeta {
	t.Helper()
	once := true
	s.WithClock(func() time.Time {
		if once {
			once = false
			return ts
		}
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

	s.WithClock(func() time.Time { return now })
	s.GC(context.Background())

	metas, _ := store.List(context.Background())
	ids := idsSet(metas)
	if !ids[m0.ID] || !ids[m1.ID] {
		t.Errorf("retainDays=0 should keep all snapshots; index: %v", metas)
	}
}

// TestGC_DeletesBeforeAnchor: with retainDays=30, the snapshot before the
// anchor is deleted; the anchor and everything newer is kept.
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

// TestGC_NoAnchorKeepsEverything: with no full snapshot old enough to be an
// anchor, nothing is deleted.
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

// TestGC_SingleOldSnapshotIsAnchorNotDeleted: a sole old full snapshot is
// the anchor and is kept.
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

// idsSet returns the set of IDs in metas.
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

// Real changes past the threshold must flush before the periodic tick;
// byte-identical upserts (status churn after sanitisation) must not.
func TestSnapshotter_BurstFlushesOnRealChangesOnly(t *testing.T) {
	buf := agent.NewBuffer()
	s := agent.NewSnapshotter(buf, newFSStore(t), time.Hour, 12).
		WithBurstFlush(3, 5*time.Millisecond)

	ready := make(chan struct{})
	close(ready)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ready) }()
	defer func() {
		cancel()
		<-done
	}()

	flushes := func() int { f, d := s.FlushCounts(); return f + d }
	waitFor := func(want int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for flushes() < want {
			if time.Now().After(deadline) {
				t.Fatalf("flushes = %d, want %d", flushes(), want)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	waitFor(1) // the initial flush

	api := k("Deployment", "default", "api")
	buf.Upsert(api, delta.State("v1"))
	for range 10 {
		buf.Upsert(api, delta.State("v1"))
	}
	// Deterministic: the counter itself. The sleep below then checks the same
	// thing end to end, as far as the scheduler lets the poll run.
	if got := buf.PendingChanges(); got != 1 {
		t.Fatalf("identical upserts counted: pending = %d, want 1", got)
	}
	time.Sleep(50 * time.Millisecond)
	if got := flushes(); got != 1 {
		t.Fatalf("identical upserts triggered a burst flush: flushes = %d, want 1", got)
	}

	buf.Upsert(api, delta.State("v2"))
	buf.Upsert(api, delta.State("v3"))
	waitFor(2)
}

// reconstructAll fails the test unless every snapshot left in store can be
// rebuilt by following its PrevID chain back to a full snapshot.
func reconstructAll(t *testing.T, store storage.Store) []types.SnapshotID {
	t.Helper()
	ctx := context.Background()
	metas, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var ids []types.SnapshotID
	for _, m := range metas {
		ids = append(ids, m.ID)
		cur := m.ID
		for {
			l, err := store.Get(ctx, cur)
			if err != nil {
				t.Errorf("snapshot %s is not reconstructable: %v", m.ID, err)
				break
			}
			if l.Meta.Kind == types.KindFull {
				break
			}
			cur = l.Meta.PrevID
		}
	}
	return ids
}

func putFull(t *testing.T, store storage.Store, ts time.Time) types.SnapshotMeta {
	t.Helper()
	m, err := store.PutFull(context.Background(), ts, delta.Snapshot{})
	if err != nil {
		t.Fatalf("PutFull: %v", err)
	}
	return m
}

func putDelta(t *testing.T, store storage.Store, ts time.Time, prev types.SnapshotMeta) types.SnapshotMeta {
	t.Helper()
	m, err := store.PutDelta(context.Background(), ts, prev.ID, delta.Delta{})
	if err != nil {
		t.Fatalf("PutDelta: %v", err)
	}
	return m
}

func runGC(store storage.Store, now time.Time, retainDays int) {
	s := agent.NewSnapshotter(agent.NewBuffer(), store, time.Minute, 3).
		WithRetention(retainDays).
		WithClock(func() time.Time { return now })
	s.GC(context.Background())
}

// The anchor must be a full snapshot. Deltas straddle the cutoff here, so
// anchoring on the newest snapshot of any kind would delete the full that
// the retained deltas are built on.
func TestGC_DeltasAcrossCutoffStayReconstructable(t *testing.T) {
	store := newFSStore(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return now.AddDate(0, 0, d) }

	f0 := putFull(t, store, day(-40))
	d1 := putDelta(t, store, day(-35), f0)
	f2 := putFull(t, store, day(-32))
	d3 := putDelta(t, store, day(-31), f2)
	d4 := putDelta(t, store, day(-29), d3)
	d5 := putDelta(t, store, day(-1), d4)

	runGC(store, now, 30)

	got := reconstructAll(t, store)
	want := []types.SnapshotID{f2.ID, d3.ID, d4.ID, d5.ID}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after GC: got %v, want %v (deleted %s, %s)", got, want, f0.ID, d1.ID)
	}
}

// Timestamps can go backwards across an agent restart (clock step, another
// node). A full written later with an earlier timestamp must not become the
// anchor that strands a retained delta built on an older full.
func TestGC_ClockStepBackKeepsRetainedDeltaReconstructable(t *testing.T) {
	store := newFSStore(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, 0, -30)

	f1 := putFull(t, store, cutoff.Add(-10*time.Minute))
	d2 := putDelta(t, store, cutoff.Add(5*time.Minute), f1) // inside the window
	// Restart on a clock that is behind: this full sorts between f1 and d2.
	f3 := putFull(t, store, cutoff.Add(-3*time.Minute))
	putDelta(t, store, now.Add(-time.Hour), f3)

	runGC(store, now, 30)

	got := reconstructAll(t, store)
	for _, id := range got {
		if id == d2.ID {
			return
		}
	}
	t.Errorf("retained delta %s was deleted; left %v", d2.ID, got)
}

type failDeleteStore struct {
	storage.Store
	fail types.SnapshotID
}

func (s failDeleteStore) Delete(ctx context.Context, id types.SnapshotID) error {
	if id == s.fail {
		return errors.New("injected delete failure")
	}
	return s.Store.Delete(ctx, id)
}

// GC is best-effort per snapshot. If one delete fails, the snapshots it
// depends on must stay, or what is left is a delta with no base.
func TestGC_FailedDeleteKeepsItsBase(t *testing.T) {
	inner := newFSStore(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return now.AddDate(0, 0, d) }

	f0 := putFull(t, inner, day(-40))
	d1 := putDelta(t, inner, day(-39), f0)
	putFull(t, inner, day(-31))

	runGC(failDeleteStore{Store: inner, fail: d1.ID}, now, 30)

	reconstructAll(t, inner)
}

// A clock step back without a restart can date a delta before its own base.
// Deleting by timestamp would remove the base first; if the delta's delete
// then fails, it is left with nothing to apply to.
func TestGC_FailedDeleteOfDeltaDatedBeforeItsBase(t *testing.T) {
	inner := newFSStore(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return now.AddDate(0, 0, d) }

	base := putFull(t, inner, day(-39))
	child := putDelta(t, inner, day(-40), base) // stepped back
	putFull(t, inner, day(-31))

	runGC(failDeleteStore{Store: inner, fail: child.ID}, now, 30)

	reconstructAll(t, inner)
}
