package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
)

// seedStore seeds a Local store under tmpDir with the given snapshots,
// returning the populated dir path so tests can pass it to run* helpers.
func seedStore(t *testing.T) (string, storage.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return dir, s
}

func TestRunSnapshotList_EmptyStore(t *testing.T) {
	dir, _ := seedStore(t)
	var buf bytes.Buffer
	if err := runSnapshotList(&buf, dir); err != nil {
		t.Fatalf("runSnapshotList: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "no snapshots") {
		t.Errorf("want 'no snapshots' message, got: %q", got)
	}
}

func TestRunSnapshotList_WithSnapshots(t *testing.T) {
	dir, store := seedStore(t)
	ctx := context.Background()
	full, _ := store.PutFull(ctx, at(2026, 5, 20, 10, 0), delta.Snapshot{
		key("Deployment", "default", "api"): delta.State("v1"),
	})
	d1, _ := store.PutDelta(ctx, at(2026, 5, 20, 10, 1), full.ID, delta.Delta{
		Modified: map[delta.Key]delta.State{
			key("Deployment", "default", "api"): delta.State("v2"),
		},
	})

	var buf bytes.Buffer
	if err := runSnapshotList(&buf, dir); err != nil {
		t.Fatalf("runSnapshotList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ID", "KIND", "TIMESTAMP", "PREV", string(full.ID), string(d1.ID), "full", "delta"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestRunSnapshotShow_Summary(t *testing.T) {
	dir, store := seedStore(t)
	full, _ := store.PutFull(context.Background(), at(2026, 5, 20, 10, 0), delta.Snapshot{
		key("Deployment", "default", "api"): delta.State(`{"hello":"world"}`),
		key("ConfigMap", "default", "cfg"):  delta.State(`{"data":{"k":"v"}}`),
	})

	var buf bytes.Buffer
	if err := runSnapshotShow(&buf, dir, full.ID, ""); err != nil {
		t.Fatalf("runSnapshotShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"KIND", "NAMESPACE", "NAME", "SIZE", "Deployment", "ConfigMap", "2 resources total"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in summary, got:\n%s", want, out)
		}
	}
}

func TestRunSnapshotShow_KeyFilter(t *testing.T) {
	dir, store := seedStore(t)
	full, _ := store.PutFull(context.Background(), at(2026, 5, 20, 10, 0), delta.Snapshot{
		key("Deployment", "default", "api"): delta.State(`{"image":"nginx:1.27","replicas":3}`),
	})

	var buf bytes.Buffer
	if err := runSnapshotShow(&buf, dir, full.ID, "Deployment/default/api"); err != nil {
		t.Fatalf("runSnapshotShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"image": "nginx:1.27"`) {
		t.Errorf("expected pretty-printed payload, got:\n%s", out)
	}
}

func TestRunSnapshotShow_KeyFilter_UnknownResource(t *testing.T) {
	dir, store := seedStore(t)
	full, _ := store.PutFull(context.Background(), at(2026, 5, 20, 10, 0), delta.Snapshot{
		key("Deployment", "default", "api"): delta.State(`{}`),
	})

	var buf bytes.Buffer
	err := runSnapshotShow(&buf, dir, full.ID, "Deployment/default/missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("want 'not found' error, got %v", err)
	}
}

func TestParseKeyFilter(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
	}{
		{"Deployment/default/api", true},
		{"ConfigMap//cfg", true},     // empty namespace is allowed
		{"Deployment/default", false}, // missing name
		{"only-one-part", false},
		{"", false},
		{"/default/api", false}, // empty kind
		{"Deployment/default/", false}, // empty name
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			_, err := parseKeyFilter(tc.in)
			gotOK := err == nil
			if gotOK != tc.wantOK {
				t.Errorf("parseKeyFilter(%q): wantOK=%v, gotOK=%v (err=%v)", tc.in, tc.wantOK, gotOK, err)
			}
		})
	}
}
