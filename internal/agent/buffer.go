// Package agent is the recorder: informers turn Kubernetes resource events
// into Buffer updates, and the Snapshotter periodically flushes the buffer
// to a storage.Store as deltas plus occasional full reference snapshots.
package agent

import (
	"bytes"
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
//
// Change counter: pendingChanges counts Upserts and Deletes that changed the
// state. An Upsert with byte-identical State — a rollout's status-only
// updates, after sanitisation — does not count, so noise alone cannot trigger
// a burst flush or inflate ktm_buffer_pending_changes. The Snapshotter
// reads it with PendingChanges and resets it with DrainChanges.
type Buffer struct {
	mu             sync.RWMutex
	state          delta.Snapshot
	pendingChanges int
}

// NewBuffer constructs an empty Buffer.
func NewBuffer() *Buffer {
	return &Buffer{state: delta.Snapshot{}}
}

// Upsert inserts or replaces the State for k. Byte-identical State is a no-op.
func (b *Buffer) Upsert(k delta.Key, s delta.State) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if old, ok := b.state[k]; ok && bytes.Equal(old, s) {
		return
	}
	b.state[k] = s
	b.pendingChanges++
}

// Delete removes k from the buffer. No-op if k is not present.
func (b *Buffer) Delete(k delta.Key) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.state[k]; !ok {
		return
	}
	delete(b.state, k)
	b.pendingChanges++
}

// PendingChanges returns the number of state-changing Upsert/Delete calls
// since the last DrainChanges. Safe to call concurrently.
func (b *Buffer) PendingChanges() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.pendingChanges
}

// DrainChanges atomically reads and resets the pending-change counter. The
// Snapshotter calls it just before each flush attempt to restart the burst
// window.
func (b *Buffer) DrainChanges() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.pendingChanges
	b.pendingChanges = 0
	return n
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

// Len returns the current number of buffered entries, exported as
// ktm_buffer_entries on /metrics.
func (b *Buffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.state)
}
