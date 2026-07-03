package storage_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// newStore opens a Local store in a fresh t.TempDir(). The directory is
// cleaned up automatically by the testing framework.
func newStore(t *testing.T) *storage.Local {
	t.Helper()
	s, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return s
}

func key(kind, ns, name string) delta.Key {
	return delta.Key{Kind: kind, Namespace: ns, Name: name}
}

// at returns a deterministic UTC timestamp so test IDs are predictable.
func at(year, month, day, hour, minute, second, millis int) time.Time {
	return time.Date(year, time.Month(month), day, hour, minute, second,
		millis*int(time.Millisecond), time.UTC)
}

func TestEmptyStore_ListIsEmpty(t *testing.T) {
	s := newStore(t)
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %v", got)
	}
}

func TestWriterLock_RejectsSecondWriterButAllowsReaders(t *testing.T) {
	root := t.TempDir()

	first, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal first: %v", err)
	}
	if err := first.AcquireWriterLock(); err != nil {
		t.Fatalf("AcquireWriterLock first: %v", err)
	}
	defer first.Close()

	// Opening the same store without acquiring the writer lock must remain
	// possible so CLI read commands can inspect history while the agent runs.
	reader, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal reader while writer active: %v", err)
	}
	if _, err := reader.List(context.Background()); err != nil {
		t.Fatalf("reader List while writer active: %v", err)
	}

	second, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal second: %v", err)
	}
	if err := second.AcquireWriterLock(); err == nil {
		_ = second.Close()
		t.Fatal("second writer acquired the same store lock")
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close first writer: %v", err)
	}
	if err := second.AcquireWriterLock(); err != nil {
		t.Fatalf("second writer did not acquire lock after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close second writer: %v", err)
	}
}

func TestPutFull_RoundTrip(t *testing.T) {
	s := newStore(t)
	snap := delta.Snapshot{
		key("Deployment", "default", "api"): delta.State("payload-api-v1"),
		key("ConfigMap", "default", "cfg"):  delta.State("payload-cfg-v1"),
	}

	meta, err := s.PutFull(context.Background(), at(2026, 5, 18, 14, 5, 30, 123), snap)
	if err != nil {
		t.Fatalf("PutFull: %v", err)
	}
	if meta.Kind != types.KindFull {
		t.Errorf("kind: want full, got %q", meta.Kind)
	}
	if meta.PrevID != "" {
		t.Errorf("PrevID should be empty for full snapshots, got %q", meta.PrevID)
	}

	loaded, err := s.Get(context.Background(), meta.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(loaded.Full, snap) {
		t.Errorf("full payload mismatch:\nwant %v\ngot  %v", snap, loaded.Full)
	}
}

func TestPutDelta_RoundTrip(t *testing.T) {
	s := newStore(t)

	full, err := s.PutFull(context.Background(), at(2026, 5, 18, 14, 0, 0, 0), delta.Snapshot{
		key("Deployment", "default", "api"): delta.State("v1"),
	})
	if err != nil {
		t.Fatalf("seed PutFull: %v", err)
	}

	d := delta.Delta{
		Added: map[delta.Key]delta.State{
			key("Service", "default", "svc"): delta.State("s1"),
		},
		Modified: map[delta.Key]delta.State{
			key("Deployment", "default", "api"): delta.State("v2"),
		},
		Removed: map[delta.Key]struct{}{
			key("ConfigMap", "default", "cfg"): {},
		},
	}

	meta, err := s.PutDelta(context.Background(), at(2026, 5, 18, 14, 5, 0, 0), full.ID, d)
	if err != nil {
		t.Fatalf("PutDelta: %v", err)
	}
	if meta.Kind != types.KindDelta {
		t.Errorf("kind: want delta, got %q", meta.Kind)
	}
	if meta.PrevID != full.ID {
		t.Errorf("PrevID: want %q, got %q", full.ID, meta.PrevID)
	}

	loaded, err := s.Get(context.Background(), meta.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(loaded.Delta, d) {
		t.Errorf("delta payload mismatch:\nwant %+v\ngot  %+v", d, loaded.Delta)
	}
}

func TestPutFull_RejectsMillisecondIDCollisionWithoutOverwrite(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ts := at(2026, 5, 18, 14, 5, 30, 123)
	first := delta.Snapshot{key("Deployment", "default", "api"): delta.State("v1")}
	second := delta.Snapshot{key("Deployment", "default", "api"): delta.State("v2")}

	meta, err := s.PutFull(ctx, ts, first)
	if err != nil {
		t.Fatalf("first PutFull: %v", err)
	}
	if _, err := s.PutFull(ctx, ts.Add(500*time.Microsecond), second); err == nil {
		t.Fatal("second PutFull in the same millisecond should report an ID collision")
	}

	loaded, err := s.Get(ctx, meta.ID)
	if err != nil {
		t.Fatalf("Get original snapshot: %v", err)
	}
	if !reflect.DeepEqual(loaded.Full, first) {
		t.Fatalf("collision overwrote original payload: want %q, got %q", first, loaded.Full)
	}
	metas, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("collision added an index entry: got %d entries", len(metas))
	}
}

func TestList_OrdersByTimestampAscending(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Put out of order; List should return ascending.
	mB, _ := s.PutFull(ctx, at(2026, 5, 18, 14, 5, 0, 0), delta.Snapshot{})
	mA, _ := s.PutFull(ctx, at(2026, 5, 18, 14, 0, 0, 0), delta.Snapshot{})
	mC, _ := s.PutFull(ctx, at(2026, 5, 18, 14, 10, 0, 0), delta.Snapshot{})

	got, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []types.SnapshotID{mA.ID, mB.ID, mC.ID}
	if len(got) != len(want) {
		t.Fatalf("len: want %d, got %d (%v)", len(want), len(got), got)
	}
	for i, m := range got {
		if m.ID != want[i] {
			t.Errorf("position %d: want %q, got %q", i, want[i], m.ID)
		}
	}
}

func TestList_ReturnsCopy(t *testing.T) {
	s := newStore(t)
	_, _ = s.PutFull(context.Background(), at(2026, 5, 18, 14, 0, 0, 0), delta.Snapshot{})

	first, _ := s.List(context.Background())
	first[0].ID = "MUTATED"

	second, _ := s.List(context.Background())
	if second[0].ID == "MUTATED" {
		t.Errorf("List returned the internal slice; mutations leaked back")
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	s1, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal #1: %v", err)
	}
	snap := delta.Snapshot{key("Deployment", "default", "api"): delta.State("v1")}
	meta, err := s1.PutFull(ctx, at(2026, 5, 18, 14, 0, 0, 0), snap)
	if err != nil {
		t.Fatalf("PutFull: %v", err)
	}

	s2, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal #2: %v", err)
	}

	list, err := s2.List(ctx)
	if err != nil {
		t.Fatalf("List on reopened store: %v", err)
	}
	if len(list) != 1 || list[0].ID != meta.ID {
		t.Errorf("reopened List: want [%s], got %v", meta.ID, list)
	}

	loaded, err := s2.Get(ctx, meta.ID)
	if err != nil {
		t.Fatalf("Get on reopened store: %v", err)
	}
	if !reflect.DeepEqual(loaded.Full, snap) {
		t.Errorf("payload mismatch after reopen:\nwant %v\ngot  %v", snap, loaded.Full)
	}
}

// TestRebuildsIndexWhenMissing exercises the "reconstructible index"
// property (ADR-0004): with index.json deleted, reopening the store
// rebuilds the index by scanning each snapshot's self-describing
// meta.json, so both Get(id) AND List/blame keep working. It also checks
// the rebuilt order (ascending by timestamp) and that index.json is
// re-persisted so the next open is a cheap read.
func TestRebuildsIndexWhenMissing(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	s1, _ := storage.NewLocal(root)
	snap := delta.Snapshot{key("Deployment", "default", "api"): delta.State("v1")}
	// Put two out of order so the rebuild's sort is actually tested.
	mB, _ := s1.PutFull(ctx, at(2026, 5, 18, 14, 5, 0, 0), snap)
	mA, _ := s1.PutFull(ctx, at(2026, 5, 18, 14, 0, 0, 0), snap)

	if err := os.Remove(filepath.Join(root, "index.json")); err != nil {
		t.Fatalf("remove index: %v", err)
	}

	s2, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal after index removal: %v", err)
	}

	// Get still works (per-snapshot meta is the source of truth).
	loaded, err := s2.Get(ctx, mA.ID)
	if err != nil {
		t.Fatalf("Get after index removal: %v", err)
	}
	if !reflect.DeepEqual(loaded.Full, snap) {
		t.Errorf("payload mismatch after index removal:\nwant %v\ngot  %v", snap, loaded.Full)
	}

	// List is now rebuilt from disk, in timestamp order.
	got, _ := s2.List(ctx)
	want := []types.SnapshotID{mA.ID, mB.ID}
	if len(got) != len(want) {
		t.Fatalf("rebuilt List len: want %d, got %d (%v)", len(want), len(got), got)
	}
	for i, m := range got {
		if m.ID != want[i] {
			t.Errorf("rebuilt List position %d: want %q, got %q", i, want[i], m.ID)
		}
	}

	// index.json was re-persisted: a third open finds it without rescanning.
	if _, err := os.Stat(filepath.Join(root, "index.json")); err != nil {
		t.Errorf("index.json not re-persisted after rebuild: %v", err)
	}
}

// TestRebuildsIndexWhenCorrupt is the sibling of the missing-index case: a
// corrupt index.json (truncated mid-write, garbled bytes) must not make
// NewLocal fail. The store recovers by rebuilding from the per-snapshot
// meta.json files and re-persists a clean index.json.
func TestRebuildsIndexWhenCorrupt(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	s1, _ := storage.NewLocal(root)
	snap := delta.Snapshot{key("Deployment", "default", "api"): delta.State("v1")}
	m, _ := s1.PutFull(ctx, at(2026, 5, 18, 14, 0, 0, 0), snap)

	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("corrupt index: %v", err)
	}

	s2, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal with a corrupt index should recover, got: %v", err)
	}
	got, _ := s2.List(ctx)
	if len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("rebuilt List after corruption: want [%s], got %v", m.ID, got)
	}

	// The corrupt cache was healed: a third open reads a valid index.json.
	s3, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal #3 after repair: %v", err)
	}
	if got3, _ := s3.List(ctx); len(got3) != 1 {
		t.Errorf("index.json not repaired; third open got %v", got3)
	}
}

func TestGet_UnknownIDReturnsError(t *testing.T) {
	s := newStore(t)
	_, err := s.Get(context.Background(), types.SnapshotID("does-not-exist"))
	if err == nil {
		t.Fatal("expected error for unknown ID, got nil")
	}
}

// TestRebuildSkipsMetaWithoutPayload ensures a crash mid-write that left
// meta.json but no payload does not pollute the rebuilt index.
func TestRebuildSkipsMetaWithoutPayload(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	s1, _ := storage.NewLocal(root)
	snap := delta.Snapshot{key("Deployment", "default", "api"): delta.State("v1")}
	good, _ := s1.PutFull(ctx, at(2026, 5, 18, 14, 0, 0, 0), snap)

	// Simulate a crash after meta was written but before payload landed.
	badDir := filepath.Join(root, "snapshots", "20260518T140500000Z")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir bad snapshot: %v", err)
	}
	badMeta := types.SnapshotMeta{
		ID:        types.SnapshotID("20260518T140500000Z"),
		Kind:      types.KindFull,
		Timestamp: at(2026, 5, 18, 14, 5, 0, 0),
	}
	metaBytes, _ := json.Marshal(badMeta)
	if err := os.WriteFile(filepath.Join(badDir, "meta.json"), metaBytes, 0o644); err != nil {
		t.Fatalf("write orphan meta: %v", err)
	}

	if err := os.Remove(filepath.Join(root, "index.json")); err != nil {
		t.Fatalf("remove index: %v", err)
	}

	s2, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal after orphan meta: %v", err)
	}
	got, _ := s2.List(ctx)
	if len(got) != 1 || got[0].ID != good.ID {
		t.Fatalf("rebuilt List: want only [%s], got %v", good.ID, got)
	}
}

// ---- Delete tests ---------------------------------------------------------

func TestDelete_RemovesFromIndexAndDisk(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	snap := delta.Snapshot{key("Pod", "default", "p"): delta.State("s")}
	meta, err := s.PutFull(ctx, at(2026, 1, 1, 0, 0, 0, 0), snap)
	if err != nil {
		t.Fatalf("PutFull: %v", err)
	}

	if err := s.Delete(ctx, meta.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Index must not contain the deleted entry.
	metas, _ := s.List(ctx)
	for _, m := range metas {
		if m.ID == meta.ID {
			t.Errorf("deleted snapshot still appears in index: %s", meta.ID)
		}
	}

	// Directory must be gone.
	dir := filepath.Join(s.Root(), "snapshots", string(meta.ID))
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("snapshot directory still exists after delete: %s", dir)
	}
}

func TestDelete_Idempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	meta, _ := s.PutFull(ctx, at(2026, 1, 1, 0, 0, 0, 0), delta.Snapshot{})
	if err := s.Delete(ctx, meta.ID); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := s.Delete(ctx, meta.ID); err != nil {
		t.Errorf("second Delete (idempotent) returned error: %v", err)
	}
}

func TestDelete_NonExistentID(t *testing.T) {
	s := newStore(t)
	if err := s.Delete(context.Background(), types.SnapshotID("doesnotexist")); err != nil {
		t.Errorf("Delete of non-existent ID returned error: %v", err)
	}
}

func TestDelete_PreservesOtherSnapshots(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	m0, _ := s.PutFull(ctx, at(2026, 1, 1, 10, 0, 0, 0), delta.Snapshot{})
	m1, _ := s.PutFull(ctx, at(2026, 1, 1, 11, 0, 0, 0), delta.Snapshot{})
	m2, _ := s.PutFull(ctx, at(2026, 1, 1, 12, 0, 0, 0), delta.Snapshot{})

	if err := s.Delete(ctx, m1.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	metas, _ := s.List(ctx)
	ids := make(map[types.SnapshotID]bool)
	for _, m := range metas {
		ids[m.ID] = true
	}
	if !ids[m0.ID] {
		t.Error("m0 should still be present after deleting m1")
	}
	if ids[m1.ID] {
		t.Error("m1 should be absent after delete")
	}
	if !ids[m2.ID] {
		t.Error("m2 should still be present after deleting m1")
	}
}

func TestDelete_IndexPersistedAfterDelete(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	s, _ := storage.NewLocal(root)
	m0, _ := s.PutFull(ctx, at(2026, 1, 1, 10, 0, 0, 0), delta.Snapshot{})
	m1, _ := s.PutFull(ctx, at(2026, 1, 1, 11, 0, 0, 0), delta.Snapshot{})

	if err := s.Delete(ctx, m0.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Re-open the store: the deleted entry must not reappear.
	s2, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("reopen NewLocal: %v", err)
	}
	metas, _ := s2.List(ctx)
	if len(metas) != 1 || metas[0].ID != m1.ID {
		t.Errorf("after reopen: expected only m1 in index, got %v", metas)
	}
}

// ---- Kind index tests -----------------------------------------------------

func TestKindsPopulatedOnPutFull(t *testing.T) {
	s := newStore(t)
	snap := delta.Snapshot{
		key("Deployment", "default", "api"): delta.State("v1"),
		key("ConfigMap", "default", "cfg"):  delta.State("c1"),
	}
	meta, err := s.PutFull(context.Background(), at(2026, 5, 18, 14, 0, 0, 0), snap)
	if err != nil {
		t.Fatalf("PutFull: %v", err)
	}
	want := []string{"ConfigMap", "Deployment"} // sorted
	if !reflect.DeepEqual(meta.Kinds, want) {
		t.Errorf("Kinds: want %v, got %v", want, meta.Kinds)
	}
}

func TestKindsPopulatedOnPutDelta(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	full, _ := s.PutFull(ctx, at(2026, 5, 18, 14, 0, 0, 0), delta.Snapshot{})
	d := delta.Delta{
		Added:    map[delta.Key]delta.State{key("Service", "default", "svc"): delta.State("s1")},
		Modified: map[delta.Key]delta.State{key("Deployment", "default", "api"): delta.State("v2")},
		Removed:  map[delta.Key]struct{}{key("ConfigMap", "default", "cfg"): {}},
	}
	meta, err := s.PutDelta(ctx, at(2026, 5, 18, 14, 5, 0, 0), full.ID, d)
	if err != nil {
		t.Fatalf("PutDelta: %v", err)
	}
	want := []string{"ConfigMap", "Deployment", "Service"} // sorted, de-duped
	if !reflect.DeepEqual(meta.Kinds, want) {
		t.Errorf("Kinds: want %v, got %v", want, meta.Kinds)
	}
}

// TestPutFull_OrphanCleanedUpOnIndexFailure verifies R-05: if writeSnapshot
// succeeds but appendIndex cannot update index.json (e.g. out of disk space),
// PutFull must roll back the snapshot directory so no orphan is left behind,
// AND the in-memory index must remain unchanged.
func TestPutFull_OrphanCleanedUpOnIndexFailure(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	s, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}

	// First snapshot writes successfully — establishes a known-good baseline.
	first, err := s.PutFull(ctx, at(2026, 1, 1, 10, 0, 0, 0), delta.Snapshot{
		key("Deployment", "default", "api"): delta.State("v1"),
	})
	if err != nil {
		t.Fatalf("seed PutFull: %v", err)
	}

	// Corrupt the index path: replace the file with a directory so any
	// subsequent attempt to write index.json fails at the rename step.
	indexPath := filepath.Join(root, "index.json")
	if err := os.Remove(indexPath); err != nil {
		t.Fatalf("remove index.json: %v", err)
	}
	if err := os.MkdirAll(indexPath, 0o755); err != nil {
		t.Fatalf("mkdir index.json: %v", err)
	}

	// Second PutFull: writeSnapshot should succeed (creates snapshots/<id>/)
	// but appendIndex must fail (can't rename over a directory).
	// The expected snapshot ID for at(2026,1,1,11,0,0,0) is "20260101T110000000Z".
	const wantOrphanID = "20260101T110000000Z"
	_, putErr := s.PutFull(ctx, at(2026, 1, 1, 11, 0, 0, 0), delta.Snapshot{})
	if putErr == nil {
		t.Fatal("expected PutFull to return an error when index write fails")
	}

	// The snapshot directory for the second write must have been removed.
	orphanDir := filepath.Join(root, "snapshots", wantOrphanID)
	if _, statErr := os.Stat(orphanDir); !os.IsNotExist(statErr) {
		t.Errorf("orphan snapshot directory must be cleaned up after appendIndex failure: %s", orphanDir)
	}

	// The in-memory index must still hold exactly the first snapshot.
	metas, _ := s.List(ctx)
	if len(metas) != 1 || metas[0].ID != first.ID {
		t.Errorf("after failed appendIndex, List: want [%s], got %v", first.ID, metas)
	}
}

// TestSerializationIsDeterministic locks down the byte-level determinism
// of the on-disk format: two semantically equal snapshots written at the
// same timestamp must produce byte-identical payload files. This matters
// for stable test fixtures and for any future content-hash-based dedup.
func TestSerializationIsDeterministic(t *testing.T) {
	ts := at(2026, 5, 18, 14, 0, 0, 0)
	snap := delta.Snapshot{
		key("ConfigMap", "default", "cfg"):  delta.State("c1"),
		key("Deployment", "default", "api"): delta.State("v1"),
		key("Service", "default", "svc"):    delta.State("s1"),
	}

	rootA := t.TempDir()
	sA, _ := storage.NewLocal(rootA)
	metaA, _ := sA.PutFull(context.Background(), ts, snap)

	rootB := t.TempDir()
	sB, _ := storage.NewLocal(rootB)
	metaB, _ := sB.PutFull(context.Background(), ts, snap)

	if metaA.ID != metaB.ID {
		t.Fatalf("same timestamp should yield same ID: %q vs %q", metaA.ID, metaB.ID)
	}
	bytesA, _ := os.ReadFile(filepath.Join(rootA, "snapshots", string(metaA.ID), "full.json"))
	bytesB, _ := os.ReadFile(filepath.Join(rootB, "snapshots", string(metaB.ID), "full.json"))
	if string(bytesA) != string(bytesB) {
		t.Errorf("payload bytes differ for equal snapshots:\nA:\n%s\nB:\n%s", bytesA, bytesB)
	}
}
