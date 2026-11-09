// Package delta computes and applies set-difference operations between
// snapshots of opaque resource state. It must stay Kubernetes-agnostic: a
// Snapshot is a map from Key to opaque State bytes.
package delta

import "bytes"

// Key identifies a single resource within a Snapshot. It must stay
// comparable so it can be a map key.
type Key struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// State is the opaque serialized payload for a single resource.
type State []byte

// Snapshot is the state of every tracked resource at a point in time.
type Snapshot map[Key]State

// Delta is the set-difference between two snapshots, split by operation.
// Compute always returns non-nil maps; test emptiness with len.
type Delta struct {
	Added    map[Key]State
	Modified map[Key]State
	Removed  map[Key]struct{}
}

// Compute returns the Delta that transforms prev into next.
// Invariant: Apply(prev, Compute(prev, next)) equals next for all inputs.
func Compute(prev, next Snapshot) Delta {
	d := Delta{
		Added:    map[Key]State{},
		Modified: map[Key]State{},
		Removed:  map[Key]struct{}{},
	}
	for k, nextState := range next {
		prevState, existed := prev[k]
		switch {
		case !existed:
			d.Added[k] = nextState
		case !bytes.Equal(prevState, nextState):
			d.Modified[k] = nextState
		}
	}
	for k := range prev {
		if _, stillThere := next[k]; !stillThere {
			d.Removed[k] = struct{}{}
		}
	}
	return d
}

// Apply returns the Snapshot that results from applying d to prev. prev is
// not mutated and the result is always a fresh map.
func Apply(prev Snapshot, d Delta) Snapshot {
	out := make(Snapshot, len(prev)+len(d.Added))
	for k, v := range prev {
		if _, removed := d.Removed[k]; removed {
			continue
		}
		out[k] = v
	}
	for k, v := range d.Added {
		out[k] = v
	}
	for k, v := range d.Modified {
		out[k] = v
	}
	return out
}
