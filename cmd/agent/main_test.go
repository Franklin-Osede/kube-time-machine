package main

import (
	"slices"
	"strings"
	"testing"
	"time"
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
