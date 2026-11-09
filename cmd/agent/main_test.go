package main

import (
	"context"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name                         string
		interval                     time.Duration
		fullEvery, burst, retainDays int
		wantErr                      string
	}{
		{name: "valid", interval: time.Second, fullEvery: 1},
		{name: "zero interval", fullEvery: 1, wantErr: "--interval"},
		{name: "negative interval", interval: -time.Second, fullEvery: 1, wantErr: "--interval"},
		{name: "sub-second interval", interval: time.Millisecond, fullEvery: 1, wantErr: "--interval"},
		{name: "zero full every", interval: time.Second, wantErr: "--full-every"},
		{name: "negative burst", interval: time.Second, fullEvery: 1, burst: -1, wantErr: "--burst-threshold"},
		{name: "negative retention", interval: time.Second, fullEvery: 1, retainDays: -1, wantErr: "--retain-days"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.interval, tt.fullEvery, tt.burst, tt.retainDays)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateConfig: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateConfig error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// A namespace that keeps a stray space would silently stop being excluded,
// and the agent would start recording it.
func TestSplitTrimmed(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{in: "", want: []string{}},
		{in: "kube-system", want: []string{"kube-system"}},
		{in: "kube-system, kube-public ,kube-node-lease", want: []string{"kube-system", "kube-public", "kube-node-lease"}},
		{in: "a,,b,", want: []string{"a", "b"}},
		{in: " , ", want: []string{}},
	}

	for _, tt := range tests {
		if got := splitTrimmed(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("splitTrimmed(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

type countingFlusher struct{ calls int }

func (f *countingFlusher) Flush(context.Context) (types.SnapshotMeta, error) {
	f.calls++
	return types.SnapshotMeta{}, nil
}

// Invariant 5: shutting down before the caches synced must not persist the
// empty buffer as a full snapshot.
func TestFinalFlush_OnlyAfterSync(t *testing.T) {
	notSynced := make(chan struct{})
	f := &countingFlusher{}
	if finalFlush(notSynced, f) || f.calls != 0 {
		t.Fatalf("flushed with caches not synced (calls=%d)", f.calls)
	}

	synced := make(chan struct{})
	close(synced)
	if !finalFlush(synced, f) || f.calls != 1 {
		t.Fatalf("did not flush after sync (calls=%d)", f.calls)
	}
}

func TestSetMemoryLimit(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	if got := setMemoryLimit(env(map[string]string{memoryLimitEnv: "268435456"})); got != 241591905 {
		t.Errorf("256Mi limit: got %d, want 90%% = 241591905", got)
	}
	if cur := debug.SetMemoryLimit(-1); cur != 241591905 {
		t.Errorf("runtime limit = %d, want it applied", cur)
	}
	debug.SetMemoryLimit(prev)
	for _, m := range []map[string]string{
		{memoryLimitEnv: "268435456", "GOMEMLIMIT": "100MiB"}, // operator's choice wins
		{memoryLimitEnv: ""},
		{memoryLimitEnv: "abc"},
		{memoryLimitEnv: "0"},
	} {
		if got := setMemoryLimit(env(m)); got != 0 || debug.SetMemoryLimit(-1) != prev {
			t.Errorf("%v: got %d and changed the runtime limit; want no change", m, got)
		}
	}
}
