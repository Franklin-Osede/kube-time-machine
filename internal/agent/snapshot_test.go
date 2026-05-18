package agent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Franklin-Osede/kube-time-machine/internal/agent"
	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

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

// TestSnapshotter_RunStopsOnContextCancel exercises the goroutine loop:
// a cancelled context must cause Run to return ctx.Err() promptly,
// without leaking the ticker goroutine.
func TestSnapshotter_RunStopsOnContextCancel(t *testing.T) {
	store := newFSStore(t)
	s := agent.NewSnapshotter(agent.NewBuffer(), store, 50*time.Millisecond, 12)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

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
