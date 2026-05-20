package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/fatih/color"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
)

func init() {
	// Force-disable ANSI colour in tests so output is stable across CI
	// runs regardless of TTY detection.
	color.NoColor = true
}

func seedDiffStore(t *testing.T) (string, storage.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return dir, s
}

func TestRunDiff_NoChanges(t *testing.T) {
	dir, store := seedDiffStore(t)
	snap := delta.Snapshot{key("Deployment", "default", "api"): delta.State(`{"v":1}`)}
	full, _ := store.PutFull(context.Background(), at(2026, 5, 20, 10, 0), snap)
	d1, _ := store.PutDelta(context.Background(), at(2026, 5, 20, 10, 1), full.ID, delta.Delta{})

	var buf bytes.Buffer
	if err := runDiff(&buf, dir, full.ID, d1.ID, ""); err != nil {
		t.Fatalf("runDiff: %v", err)
	}
	if !strings.Contains(buf.String(), "no changes") {
		t.Errorf("want 'no changes' message, got: %q", buf.String())
	}
}

func TestRunDiff_AddedOnly(t *testing.T) {
	dir, store := seedDiffStore(t)
	ctx := context.Background()
	s0 := delta.Snapshot{}
	s1 := delta.Snapshot{key("Deployment", "default", "api"): delta.State(`{"image":"nginx:1.27"}`)}

	full, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 0), s0)
	d, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 1), full.ID, delta.Compute(s0, s1))

	var buf bytes.Buffer
	if err := runDiff(&buf, dir, full.ID, d.ID, ""); err != nil {
		t.Fatalf("runDiff: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Added (1)", "+ Deployment/default/api", `"image": "nginx:1.27"`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

func TestRunDiff_ModifiedShowsUnifiedDiff(t *testing.T) {
	dir, store := seedDiffStore(t)
	ctx := context.Background()
	s0 := delta.Snapshot{key("Deployment", "default", "api"): delta.State(`{"image":"nginx:1.25","replicas":2}`)}
	s1 := delta.Snapshot{key("Deployment", "default", "api"): delta.State(`{"image":"nginx:1.27","replicas":2}`)}

	full, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 0), s0)
	d, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 1), full.ID, delta.Compute(s0, s1))

	var buf bytes.Buffer
	if err := runDiff(&buf, dir, full.ID, d.ID, ""); err != nil {
		t.Fatalf("runDiff: %v", err)
	}
	out := buf.String()
	// Should see both the old and the new image lines in the unified diff.
	for _, want := range []string{"Modified (1)", "~ Deployment/default/api", `-  "image": "nginx:1.25"`, `+  "image": "nginx:1.27"`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in diff output:\n%s", want, out)
		}
	}
}

func TestRunDiff_RemovedOnly(t *testing.T) {
	dir, store := seedDiffStore(t)
	ctx := context.Background()
	s0 := delta.Snapshot{key("ConfigMap", "default", "cfg"): delta.State(`{"data":{"k":"v"}}`)}
	s1 := delta.Snapshot{}

	full, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 0), s0)
	d, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 1), full.ID, delta.Compute(s0, s1))

	var buf bytes.Buffer
	if err := runDiff(&buf, dir, full.ID, d.ID, ""); err != nil {
		t.Fatalf("runDiff: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Removed (1)", "- ConfigMap/default/cfg"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

func TestRunDiff_NamespaceFilter(t *testing.T) {
	dir, store := seedDiffStore(t)
	ctx := context.Background()
	s0 := delta.Snapshot{
		key("Deployment", "team-a", "api"): delta.State(`{"v":1}`),
		key("Deployment", "team-b", "api"): delta.State(`{"v":1}`),
	}
	s1 := delta.Snapshot{
		key("Deployment", "team-a", "api"): delta.State(`{"v":2}`),
		key("Deployment", "team-b", "api"): delta.State(`{"v":2}`),
	}
	full, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 0), s0)
	d, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 1), full.ID, delta.Compute(s0, s1))

	var buf bytes.Buffer
	if err := runDiff(&buf, dir, full.ID, d.ID, "team-a"); err != nil {
		t.Fatalf("runDiff: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Deployment/team-a/api") {
		t.Errorf("team-a should be present:\n%s", out)
	}
	if strings.Contains(out, "team-b") {
		t.Errorf("team-b should be filtered out, but appears:\n%s", out)
	}
}

func TestRunDiff_MissingFromOrToErrors(t *testing.T) {
	dir, store := seedDiffStore(t)
	full, _ := store.PutFull(context.Background(), at(2026, 5, 20, 10, 0), delta.Snapshot{})

	var buf bytes.Buffer
	if err := runDiff(&buf, dir, full.ID, "", ""); err == nil {
		t.Error("expected error when --to is empty")
	}
}

func TestFilterByNamespace_DoesNotMutateInput(t *testing.T) {
	in := delta.Snapshot{
		key("Deployment", "team-a", "api"): delta.State("v1"),
		key("Deployment", "team-b", "api"): delta.State("v1"),
	}
	out := filterByNamespace(in, "team-a")
	if len(out) != 1 {
		t.Errorf("filtered map should have 1 entry, got %d", len(out))
	}
	if len(in) != 2 {
		t.Errorf("input map mutated: want len 2, got %d", len(in))
	}
}
