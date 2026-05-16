// Package delta computes and applies set-difference operations between
// snapshots of opaque resource state.
//
// The package is intentionally Kubernetes-agnostic: a Snapshot is just a
// map from Key to opaque State bytes. Serialization, persistence, and the
// concrete shape of "a Kubernetes resource" live elsewhere.
package delta

import "bytes"

// Key identifies a single resource within a Snapshot. All fields are
// strings, which makes Key comparable and therefore usable directly as a
// map key — no string concatenation, no hashing helper.
type Key struct {
	Kind      string
	Namespace string
	Name      string
}

// State is the opaque serialized payload for a single resource. The delta
// package treats it as a bag of bytes; it does not parse or interpret it.
type State []byte

// Snapshot is the state of every tracked resource at a point in time.
type Snapshot map[Key]State

// Delta is the set-difference between two snapshots, split by operation
// so Apply can be implemented as three straightforward passes.
//
// The maps are always non-nil after a call to Compute, even when empty.
// Callers should test emptiness with len(...) rather than == nil.
type Delta struct {
	Added    map[Key]State
	Modified map[Key]State
	Removed  map[Key]struct{}
}

// Compute returns the Delta that transforms prev into next.
//
// Invariant (verified by tests): Apply(prev, Compute(prev, next)) equals
// next for all prev, next.
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

// Apply returns the Snapshot that results from applying d to prev.
// prev is not mutated; the returned Snapshot is always a fresh map so
// callers can safely chain Apply calls without aliasing concerns.
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
