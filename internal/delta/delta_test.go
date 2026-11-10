package delta_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
)

// snap builds a Snapshot from alternating "Kind/Namespace/Name" and payload
// arguments.
func snap(pairs ...string) delta.Snapshot {
	if len(pairs)%2 != 0 {
		panic("snap: arguments must come in (key, state) pairs")
	}
	out := delta.Snapshot{}
	for i := 0; i < len(pairs); i += 2 {
		out[parseKey(pairs[i])] = delta.State(pairs[i+1])
	}
	return out
}

func parseKey(s string) delta.Key {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) != 3 {
		panic("parseKey: want Kind/Namespace/Name, got " + s)
	}
	return delta.Key{Kind: parts[0], Namespace: parts[1], Name: parts[2]}
}

// TestComputeProducesExpectedDelta checks that entries land in Added,
// Modified and Removed; TestRoundTrip checks behaviour.
func TestComputeProducesExpectedDelta(t *testing.T) {
	prev := snap("A/n/1", "v1", "B/n/2", "v2")
	next := snap(
		"A/n/1", "v1-new", // modified
		"C/n/3", "v3", // added; B/n/2 disappears (removed)
	)

	d := delta.Compute(prev, next)

	wantAdded := map[delta.Key]delta.State{
		{Kind: "C", Namespace: "n", Name: "3"}: delta.State("v3"),
	}
	wantModified := map[delta.Key]delta.State{
		{Kind: "A", Namespace: "n", Name: "1"}: delta.State("v1-new"),
	}
	wantRemoved := map[delta.Key]struct{}{
		{Kind: "B", Namespace: "n", Name: "2"}: {},
	}

	if !reflect.DeepEqual(d.Added, wantAdded) {
		t.Errorf("Added: want %v, got %v", wantAdded, d.Added)
	}
	if !reflect.DeepEqual(d.Modified, wantModified) {
		t.Errorf("Modified: want %v, got %v", wantModified, d.Modified)
	}
	if !reflect.DeepEqual(d.Removed, wantRemoved) {
		t.Errorf("Removed: want %v, got %v", wantRemoved, d.Removed)
	}
}

// TestRoundTrip guards the package invariant:
// Apply(prev, Compute(prev, next)) == next.
func TestRoundTrip(t *testing.T) {
	cases := []struct {
		name       string
		prev, next delta.Snapshot
	}{
		{"both empty", snap(), snap()},
		{"add to empty", snap(), snap("Deployment/default/api", "v1")},
		{"remove everything", snap("Deployment/default/api", "v1"), snap()},
		{"modify in place", snap("Deployment/default/api", "v1"), snap("Deployment/default/api", "v2")},
		{"identity", snap("X/y/z", "v"), snap("X/y/z", "v")},
		{"mixed add + modify + remove",
			snap(
				"Deployment/default/api", "v1",
				"ConfigMap/default/cfg", "c1",
			),
			snap(
				"Deployment/default/api", "v2", // modified
				"Deployment/default/worker", "w1", // added
				// configmap removed
			),
		},
		{"same kind, different namespaces",
			snap(
				"Deployment/prod/api", "v1",
				"Deployment/stage/api", "v1",
			),
			snap(
				"Deployment/prod/api", "v2", // modified
				// stage/api removed
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := delta.Compute(tc.prev, tc.next)
			got := delta.Apply(tc.prev, d)
			if !reflect.DeepEqual(got, tc.next) {
				t.Errorf("round-trip mismatch\nprev:  %v\nnext:  %v\ndelta: %+v\ngot:   %v",
					tc.prev, tc.next, d, got)
			}
		})
	}
}

// TestApplyChain replays chained deltas on a full snapshot, as reconstruction
// does.
func TestApplyChain(t *testing.T) {
	s0 := snap()
	s1 := snap("Deployment/default/api", "v1")
	s2 := snap(
		"Deployment/default/api", "v1",
		"ConfigMap/default/cfg", "c1",
	)
	s3 := snap(
		"Deployment/default/api", "v2", // modified
		"ConfigMap/default/cfg", "c1",
	)
	s4 := snap(
		"Deployment/default/api", "v2",
		// cfg removed
	)

	d1 := delta.Compute(s0, s1)
	d2 := delta.Compute(s1, s2)
	d3 := delta.Compute(s2, s3)
	d4 := delta.Compute(s3, s4)

	got := delta.Apply(delta.Apply(delta.Apply(delta.Apply(s0, d1), d2), d3), d4)
	if !reflect.DeepEqual(got, s4) {
		t.Errorf("chain reconstruction failed\nwant: %v\ngot:  %v", s4, got)
	}
}

// TestApplyDoesNotMutatePrev: Apply returns a fresh map, so chained
// reconstruction cannot corrupt snapshots callers still hold.
func TestApplyDoesNotMutatePrev(t *testing.T) {
	prev := snap("A/n/1", "v1")
	d := delta.Compute(prev, snap("A/n/1", "v2"))

	_ = delta.Apply(prev, d)

	want := snap("A/n/1", "v1")
	if !reflect.DeepEqual(prev, want) {
		t.Errorf("Apply mutated prev: want %v, got %v", want, prev)
	}
}

// TestIdentityProducesEmptyDelta: on a quiet cluster deltas must be empty.
func TestIdentityProducesEmptyDelta(t *testing.T) {
	s := snap(
		"Deployment/default/api", "v1",
		"ConfigMap/default/cfg", "c1",
	)
	d := delta.Compute(s, s)

	if len(d.Added) != 0 || len(d.Modified) != 0 || len(d.Removed) != 0 {
		t.Errorf("identity compute produced non-empty delta: %+v", d)
	}
}
