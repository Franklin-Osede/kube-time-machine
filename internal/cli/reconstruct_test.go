package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// White-box test (package cli, not cli_test) so we can call the
// unexported reconstruct helper directly. The function is intentionally
// unexported — it's an implementation detail of show, diff, and (Etapa 5)
// blame.

func key(kind, ns, name string) delta.Key {
	return delta.Key{Kind: kind, Namespace: ns, Name: name}
}

func at(year, month, day, hour, minute int) time.Time {
	return time.Date(year, time.Month(month), day, hour, minute, 0, 0, time.UTC)
}

func newStore(t *testing.T) storage.Store {
	t.Helper()
	s, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return s
}

func TestReconstruct_FullOnly(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	snap := delta.Snapshot{
		key("Deployment", "default", "api"): delta.State("v1"),
		key("ConfigMap", "default", "cfg"):  delta.State("c1"),
	}
	meta, err := store.PutFull(ctx, at(2026, 5, 20, 10, 0), snap)
	if err != nil {
		t.Fatalf("PutFull: %v", err)
	}

	got, err := reconstruct(ctx, store, meta.ID)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if !reflect.DeepEqual(got, snap) {
		t.Errorf("reconstruct mismatch:\nwant %v\ngot  %v", snap, got)
	}
}

func TestReconstruct_FullPlusDeltas(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	// Seed a chain: full → d1 → d2 → d3
	s0 := delta.Snapshot{
		key("Deployment", "default", "api"): delta.State("v1"),
	}
	full, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 0), s0)

	s1 := delta.Snapshot{
		key("Deployment", "default", "api"): delta.State("v1"),
		key("ConfigMap", "default", "cfg"):  delta.State("c1"),
	}
	d1, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 1), full.ID, delta.Compute(s0, s1))

	s2 := delta.Snapshot{
		key("Deployment", "default", "api"): delta.State("v2"), // modified
		key("ConfigMap", "default", "cfg"):  delta.State("c1"),
	}
	d2, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 2), d1.ID, delta.Compute(s1, s2))

	s3 := delta.Snapshot{
		key("Deployment", "default", "api"): delta.State("v2"),
		// cfg removed
	}
	d3, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 3), d2.ID, delta.Compute(s2, s3))

	// Reconstruct each point should match the original snapshot at that point.
	for i, tc := range []struct {
		id   types.SnapshotID
		want delta.Snapshot
		name string
	}{
		{full.ID, s0, "full"},
		{d1.ID, s1, "d1"},
		{d2.ID, s2, "d2"},
		{d3.ID, s3, "d3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := reconstruct(ctx, store, tc.id)
			if err != nil {
				t.Fatalf("reconstruct %s: %v", tc.id, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("step %d (%s):\nwant %v\ngot  %v", i, tc.name, tc.want, got)
			}
		})
	}
}

func TestReconstruct_EmptyIDErrors(t *testing.T) {
	if _, err := reconstruct(context.Background(), newStore(t), ""); err == nil {
		t.Error("expected error for empty id, got nil")
	}
}

func TestReconstruct_UnknownIDErrors(t *testing.T) {
	if _, err := reconstruct(context.Background(), newStore(t), types.SnapshotID("does-not-exist")); err == nil {
		t.Error("expected error for unknown id, got nil")
	}
}

// TestReconstruct_CyclicChainReturnsError verifies that a corrupt delta chain
// where PrevID forms a cycle (A→B→A) is detected quickly instead of looping
// indefinitely. The store is seeded by writing meta.json and delta.json
// directly to disk, bypassing the PutDelta API which cannot create cycles.
func TestReconstruct_CyclicChainReturnsError(t *testing.T) {
	root := t.TempDir()

	var (
		idA = types.SnapshotID("20260101T000000000Z")
		idB = types.SnapshotID("20260101T000000001Z")
	)
	// Write two delta snapshots whose PrevID fields form a cycle.
	for _, pair := range [][2]types.SnapshotID{{idA, idB}, {idB, idA}} {
		id, prev := pair[0], pair[1]
		dir := filepath.Join(root, "snapshots", string(id))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		meta := types.SnapshotMeta{
			ID:        id,
			Kind:      types.KindDelta,
			PrevID:    prev,
			Timestamp: at(2026, 1, 1, 0, 0),
		}
		b, _ := json.Marshal(meta)
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o600); err != nil {
			t.Fatalf("write meta: %v", err)
		}
		// wireDelta with omitempty: all-empty fields marshal to {}
		if err := os.WriteFile(filepath.Join(dir, "delta.json"), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write delta: %v", err)
		}
	}

	store, err := storage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}

	// Run reconstruct in a goroutine; 2-second outer timer catches any hang.
	timeout, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := reconstruct(context.Background(), store, idA)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error for cyclic chain, got nil")
		}
		if !strings.Contains(err.Error(), "cycle") {
			t.Errorf("expected 'cycle' in error, got: %v", err)
		}
	case <-timeout.Done():
		t.Error("reconstruct hung indefinitely on a cyclic chain — cycle guard missing?")
	}
}
