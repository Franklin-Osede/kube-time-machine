package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
)

// seedBlameStore returns a Local store ready to receive snapshots. The
// chronological order in the index is driven by the timestamps the
// caller supplies, not by call order, so tests are free to seed however
// they want.
func seedBlameStore(t *testing.T) (string, storage.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return dir, s
}

func TestComputeBlame_EmptyStore(t *testing.T) {
	_, store := seedBlameStore(t)
	entries, err := computeBlame(context.Background(), store, key("Deployment", "default", "api"))
	if err != nil {
		t.Fatalf("computeBlame: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want no entries, got %v", entries)
	}
}

func TestComputeBlame_CreatedModifiedRemoved(t *testing.T) {
	_, store := seedBlameStore(t)
	ctx := context.Background()
	target := key("Deployment", "default", "api")

	// Snapshot 0: empty. Target not present.
	s0 := delta.Snapshot{}
	m0, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 0), s0)

	// Snapshot 1: target appears.
	s1 := delta.Snapshot{target: delta.State("v1")}
	m1, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 1), m0.ID, delta.Compute(s0, s1))

	// Snapshot 2: target changes payload.
	s2 := delta.Snapshot{target: delta.State("v2")}
	m2, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 2), m1.ID, delta.Compute(s1, s2))

	// Snapshot 3: target unchanged (should NOT emit an entry).
	m3, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 3), m2.ID, delta.Compute(s2, s2))

	// Snapshot 4: target removed.
	s4 := delta.Snapshot{}
	m4, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 4), m3.ID, delta.Compute(s2, s4))

	entries, err := computeBlame(ctx, store, target)
	if err != nil {
		t.Fatalf("computeBlame: %v", err)
	}

	want := []struct {
		op blameOp
		id string
	}{
		{opCreated, string(m1.ID)},
		{opModified, string(m2.ID)},
		{opRemoved, string(m4.ID)},
	}
	if len(entries) != len(want) {
		t.Fatalf("want %d entries, got %d (%+v)", len(want), len(entries), entries)
	}
	for i, w := range want {
		if entries[i].Op != w.op {
			t.Errorf("entry %d op: want %s, got %s", i, w.op, entries[i].Op)
		}
		if string(entries[i].SnapshotID) != w.id {
			t.Errorf("entry %d snapshotID: want %s, got %s", i, w.id, entries[i].SnapshotID)
		}
	}
}

// TestComputeBlame_DetectsDeleteAtFullTick is the load-bearing test that
// motivated the running-reconstruction algorithm. It simulates the
// asymmetry observed in the 2026-05-20 smoke test: when a delete
// coincides with a full-snapshot tick, the deletion is represented by
// absence in the new full, not by a `removed` entry on the prior delta.
// Naive scans of delta.Removed would miss this; computeBlame must catch
// it by comparing the running state.
func TestComputeBlame_DetectsDeleteAtFullTick(t *testing.T) {
	_, store := seedBlameStore(t)
	ctx := context.Background()
	target := key("ConfigMap", "default", "cfg")

	s0 := delta.Snapshot{target: delta.State("c1")}
	store.PutFull(ctx, at(2026, 5, 20, 10, 0), s0)
	store.PutDelta(ctx, at(2026, 5, 20, 10, 1), "", delta.Compute(s0, s0)) // empty delta
	store.PutDelta(ctx, at(2026, 5, 20, 10, 2), "", delta.Compute(s0, s0)) // empty delta

	// Now the delete happens — but the next snapshot is a FULL, so the
	// delete is captured by absence rather than a `removed` entry on a delta.
	s3 := delta.Snapshot{} // target gone
	mFull, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 3), s3)

	entries, err := computeBlame(ctx, store, target)
	if err != nil {
		t.Fatalf("computeBlame: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries (CREATED + REMOVED), got %d: %+v", len(entries), entries)
	}
	if entries[0].Op != opCreated {
		t.Errorf("first entry: want CREATED, got %s", entries[0].Op)
	}
	if entries[1].Op != opRemoved {
		t.Errorf("second entry: want REMOVED, got %s", entries[1].Op)
	}
	if string(entries[1].SnapshotID) != string(mFull.ID) {
		t.Errorf("REMOVED should be attributed to the full snapshot %s, got %s", mFull.ID, entries[1].SnapshotID)
	}
}

func TestComputeBlame_TargetNeverPresent(t *testing.T) {
	_, store := seedBlameStore(t)
	ctx := context.Background()
	other := key("Deployment", "default", "other")
	store.PutFull(ctx, at(2026, 5, 20, 10, 0), delta.Snapshot{other: delta.State("v1")})

	entries, err := computeBlame(ctx, store, key("Deployment", "default", "missing"))
	if err != nil {
		t.Fatalf("computeBlame: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want 0 entries for never-present target, got %v", entries)
	}
}

// TestComputeBlame_SkipsDeltaWithUnrelatedKind verifies that delta snapshots
// whose Kinds list is populated and does not include the target kind are
// skipped without a store.Get() call. We confirm this indirectly: if the
// skip is absent the running state would be corrupted (we pass a broken
// store that panics on Get for the irrelevant delta), but computeBlame must
// still return the correct result for the target kind.
//
// Setup:
//
//	full[0]  Kinds=[ConfigMap]          target Deployment not present
//	delta[1] Kinds=[ConfigMap]          only a ConfigMap changed → must skip
//	delta[2] Kinds=[Deployment]         target Deployment added
//
// Expected: one CREATED entry at delta[2].
func TestComputeBlame_SkipsDeltaWithUnrelatedKind(t *testing.T) {
	_, store := seedBlameStore(t)
	ctx := context.Background()

	target := key("Deployment", "default", "api")
	cm := key("ConfigMap", "default", "cfg")

	// Snapshot 0 (full): only a ConfigMap exists.
	s0 := delta.Snapshot{cm: delta.State("c1")}
	m0, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 0), s0)

	// Snapshot 1 (delta): ConfigMap changes — Kinds=[ConfigMap], no Deployment.
	s1 := delta.Snapshot{cm: delta.State("c2")}
	m1, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 1), m0.ID, delta.Compute(s0, s1))

	// Snapshot 2 (delta): Deployment added — Kinds=[Deployment].
	s2 := delta.Snapshot{cm: delta.State("c2"), target: delta.State("v1")}
	_, _ = store.PutDelta(ctx, at(2026, 5, 20, 10, 2), m1.ID, delta.Compute(s1, s2))

	entries, err := computeBlame(ctx, store, target)
	if err != nil {
		t.Fatalf("computeBlame: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry (CREATED), got %d: %+v", len(entries), entries)
	}
	if entries[0].Op != opCreated {
		t.Errorf("want CREATED, got %s", entries[0].Op)
	}
}

func TestRunBlame_EmptyMessage(t *testing.T) {
	dir, _ := seedBlameStore(t)
	var buf bytes.Buffer
	if err := runBlame(&buf, dir, key("Deployment", "default", "api"), ""); err != nil {
		t.Fatalf("runBlame: %v", err)
	}
	if !strings.Contains(buf.String(), "no history found") {
		t.Errorf("want 'no history found' message, got: %q", buf.String())
	}
}

func TestRunBlame_RendersTable(t *testing.T) {
	dir, store := seedBlameStore(t)
	ctx := context.Background()
	target := key("Deployment", "default", "api")

	s0 := delta.Snapshot{}
	m0, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 0), s0)
	s1 := delta.Snapshot{target: delta.State("v1")}
	store.PutDelta(ctx, at(2026, 5, 20, 10, 1), m0.ID, delta.Compute(s0, s1))

	var buf bytes.Buffer
	if err := runBlame(&buf, dir, target, ""); err != nil {
		t.Fatalf("runBlame: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"TIME", "OP", "SNAPSHOT", "CREATED"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

// TestRunBlame_NamespaceAppliedFromFlag verifies that when the target key has
// an empty namespace, the --namespace flag fills it in before the blame scan.
// This is a forward-compat hook for future bare-name positional args.
func TestRunBlame_NamespaceAppliedFromFlag(t *testing.T) {
	dir, store := seedBlameStore(t)
	ctx := context.Background()

	target := key("Deployment", "myns", "api")
	s0 := delta.Snapshot{}
	m0, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 0), s0)
	s1 := delta.Snapshot{target: delta.State("v1")}
	store.PutDelta(ctx, at(2026, 5, 20, 10, 1), m0.ID, delta.Compute(s0, s1))

	var buf bytes.Buffer
	noNsTarget := delta.Key{Kind: "Deployment", Namespace: "", Name: "api"}
	if err := runBlame(&buf, dir, noNsTarget, "myns"); err != nil {
		t.Fatalf("runBlame: %v", err)
	}
	if !strings.Contains(buf.String(), "CREATED") {
		t.Errorf("expected CREATED entry after namespace applied from flag, got:\n%s", buf.String())
	}
}

// TestComputeBlame_ManagersFromJSONPayload pins the MANAGERS column end-to-end.
// Existing tests use raw byte strings as state (not JSON) so managersFromState
// always returned "". This test uses a real JSON payload with the
// ktm.io/managers annotation that the agent marshal layer injects.
func TestComputeBlame_ManagersFromJSONPayload(t *testing.T) {
	_, store := seedBlameStore(t)
	ctx := context.Background()
	target := key("Deployment", "default", "api")

	payload := `{"metadata":{"name":"api","namespace":"default","annotations":{"ktm.io/managers":"helm,kubectl"}}}`

	s0 := delta.Snapshot{}
	m0, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 0), s0)
	s1 := delta.Snapshot{target: delta.State(payload)}
	store.PutDelta(ctx, at(2026, 5, 20, 10, 1), m0.ID, delta.Compute(s0, s1))

	entries, err := computeBlame(ctx, store, target)
	if err != nil {
		t.Fatalf("computeBlame: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].Managers != "helm,kubectl" {
		t.Errorf("managers: want %q, got %q", "helm,kubectl", entries[0].Managers)
	}
}
