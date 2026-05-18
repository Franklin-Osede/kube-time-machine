package agent_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/Franklin-Osede/kube-time-machine/internal/agent"
	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
)

func k(kind, ns, name string) delta.Key {
	return delta.Key{Kind: kind, Namespace: ns, Name: name}
}

func TestBuffer_EmptyOnConstruction(t *testing.T) {
	b := agent.NewBuffer()
	if got := b.Len(); got != 0 {
		t.Errorf("Len: want 0, got %d", got)
	}
	if got := b.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot: want empty, got %v", got)
	}
}

func TestBuffer_UpsertAndSnapshot(t *testing.T) {
	b := agent.NewBuffer()
	b.Upsert(k("Deployment", "default", "api"), delta.State("v1"))
	b.Upsert(k("ConfigMap", "default", "cfg"), delta.State("c1"))

	snap := b.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("want 2 entries, got %d (%v)", len(snap), snap)
	}
	if got := snap[k("Deployment", "default", "api")]; string(got) != "v1" {
		t.Errorf("Deployment state: want v1, got %q", got)
	}
}

func TestBuffer_UpsertReplaces(t *testing.T) {
	b := agent.NewBuffer()
	key := k("Deployment", "default", "api")
	b.Upsert(key, delta.State("v1"))
	b.Upsert(key, delta.State("v2"))

	snap := b.Snapshot()
	if got := snap[key]; string(got) != "v2" {
		t.Errorf("after replace: want v2, got %q", got)
	}
	if got := b.Len(); got != 1 {
		t.Errorf("Len after replace: want 1, got %d", got)
	}
}

func TestBuffer_Delete(t *testing.T) {
	b := agent.NewBuffer()
	key := k("Deployment", "default", "api")
	b.Upsert(key, delta.State("v1"))
	b.Delete(key)

	if got := b.Len(); got != 0 {
		t.Errorf("Len after delete: want 0, got %d", got)
	}
	snap := b.Snapshot()
	if _, ok := snap[key]; ok {
		t.Errorf("deleted key still present: %v", snap)
	}
}

func TestBuffer_DeleteAbsentIsNoop(t *testing.T) {
	b := agent.NewBuffer()
	// Should not panic, should not error.
	b.Delete(k("Deployment", "default", "missing"))
	if got := b.Len(); got != 0 {
		t.Errorf("Len: want 0, got %d", got)
	}
}

// TestBuffer_SnapshotReturnsIndependentMap guards the contract: callers
// can freely mutate the returned map without affecting the buffer.
func TestBuffer_SnapshotReturnsIndependentMap(t *testing.T) {
	b := agent.NewBuffer()
	key := k("Deployment", "default", "api")
	b.Upsert(key, delta.State("v1"))

	snap1 := b.Snapshot()
	snap1[k("Injected", "x", "y")] = delta.State("evil")
	delete(snap1, key)

	snap2 := b.Snapshot()
	if _, ok := snap2[k("Injected", "x", "y")]; ok {
		t.Errorf("mutation of returned snapshot leaked back into buffer")
	}
	if _, ok := snap2[key]; !ok {
		t.Errorf("original key disappeared from buffer after caller deleted from copy")
	}
}

// TestBuffer_ConcurrentAccessIsRaceFree exercises the lock under
// contention. Must be run with `go test -race` to be meaningful; without
// -race this only verifies that the final state is consistent.
func TestBuffer_ConcurrentAccessIsRaceFree(t *testing.T) {
	b := agent.NewBuffer()
	const writers = 8
	const opsPerWriter = 500

	var wg sync.WaitGroup
	wg.Add(writers + 1)

	// Writers: each upserts a disjoint slice of keys, then deletes half.
	for w := 0; w < writers; w++ {
		w := w
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerWriter; i++ {
				key := k("Deployment", "default", fmt.Sprintf("w%d-%d", w, i))
				b.Upsert(key, delta.State(fmt.Sprintf("v%d", i)))
				if i%2 == 0 {
					b.Delete(key)
				}
			}
		}()
	}

	// Reader: hammers Snapshot while writers are active.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = b.Snapshot()
		}
	}()

	wg.Wait()

	// Expected final size: writers * opsPerWriter / 2  (the odd-i entries that survive Delete).
	want := writers * opsPerWriter / 2
	if got := b.Len(); got != want {
		t.Errorf("final Len: want %d, got %d", want, got)
	}
}
