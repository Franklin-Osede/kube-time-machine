// Package agent is the recorder: informers turn Kubernetes resource events
// into Buffer updates, and the Snapshotter periodically flushes the buffer
// to a storage.Store as deltas plus occasional full reference snapshots.
package agent

import (
	"bytes"
	"sync"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
)

// Buffer holds the current known state of every watched resource. It is
// safe for concurrent use by the informer handlers and the Snapshotter.
//
// pendingChanges counts only Upserts and Deletes that changed the state, so
// status-only updates, identical after sanitisation, cannot trigger a burst
// flush.
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
// since the last SnapshotAndDrain. Safe to call concurrently.
func (b *Buffer) PendingChanges() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.pendingChanges
}

// SnapshotAndDrain returns a copy of the state and resets the change counter
// under the same lock, returning the count it cleared, so no change is
// counted twice.
func (b *Buffer) SnapshotAndDrain() (delta.Snapshot, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(delta.Snapshot, len(b.state))
	for k, v := range b.state {
		out[k] = v
	}
	n := b.pendingChanges
	b.pendingChanges = 0
	return out, n
}

// Snapshot returns a copy of the current state. The map is the caller's, but
// the State byte slices are shared and must not be mutated.
func (b *Buffer) Snapshot() delta.Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(delta.Snapshot, len(b.state))
	for k, v := range b.state {
		out[k] = v
	}
	return out
}

// Len returns the current number of buffered entries.
func (b *Buffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.state)
}
