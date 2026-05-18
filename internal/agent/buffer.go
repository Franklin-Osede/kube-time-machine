// Package agent contains the in-cluster agent: the informer wiring that
// turns Kubernetes resource events into delta.Snapshot mutations, the
// in-memory buffer that holds the current known state, and the periodic
// snapshotter that flushes deltas (and occasional full reference
// snapshots) to a storage.Store.
//
// This file defines the Buffer. The informer wiring lives in
// informers.go; the periodic snapshotter lives in snapshot.go.
package agent

import (
	"sync"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
)

// Buffer holds the current known state of every watched resource.
//
// It is safe for concurrent use: informer event handlers call Upsert and
// Delete from multiple goroutines (one per informer), while the
// Snapshotter calls Snapshot from its single flush goroutine.
//
// Locking: a sync.RWMutex protects state. Writes (Upsert, Delete) take
// the write lock; reads (Snapshot, Len) take the read lock. The pattern
// fits our workload — many small writes, infrequent but bulky reads.
type Buffer struct {
	mu    sync.RWMutex
	state delta.Snapshot
}

// NewBuffer constructs an empty Buffer.
func NewBuffer() *Buffer {
	return &Buffer{state: delta.Snapshot{}}
}

// Upsert inserts or replaces the State for k.
func (b *Buffer) Upsert(k delta.Key, s delta.State) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state[k] = s
}

// Delete removes k from the buffer. No-op if k is not present.
func (b *Buffer) Delete(k delta.Key) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.state, k)
}

// Snapshot returns a copy of the current state. The returned map is
// owned by the caller — mutating it does not affect the buffer.
//
// The State byte slices are NOT deep-copied. By convention, informer
// handlers always Upsert a freshly-marshalled payload rather than
// mutating one in place, so the slices are effectively immutable. If a
// future caller needs to mutate State bytes it must copy them first.
func (b *Buffer) Snapshot() delta.Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(delta.Snapshot, len(b.state))
	for k, v := range b.state {
		out[k] = v
	}
	return out
}

// Len returns the current number of buffered entries. Useful for tests,
// readiness probes, and future /metrics endpoints.
func (b *Buffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.state)
}
