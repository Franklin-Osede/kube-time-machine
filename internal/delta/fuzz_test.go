package delta_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
)

// FuzzRoundTrip fuzzes Apply(prev, Compute(prev, next)) == next.
// Run with: go test -fuzz=FuzzRoundTrip -fuzztime=30s ./internal/delta/
func FuzzRoundTrip(f *testing.F) {
	f.Add( // typical: modify-and-add-and-remove
		"Deployment/default/api", []byte("v1"),
		"ConfigMap/default/cfg", []byte("c1"),
		"Deployment/default/api", []byte("v2"), // modified
		"Service/default/svc", []byte("s1"), // added; cfg removed
	)
	f.Add( // edge: all empty strings, empty payloads
		"", []byte{}, "", []byte{}, "", []byte{}, "", []byte{},
	)
	f.Add( // edge: identical snapshots (Compute must produce empty delta)
		"A/n/1", []byte("x"), "B/n/2", []byte("y"),
		"A/n/1", []byte("x"), "B/n/2", []byte("y"),
	)

	f.Fuzz(func(t *testing.T,
		kA string, sA []byte,
		kB string, sB []byte,
		kC string, sC []byte,
		kD string, sD []byte,
	) {
		// Colliding keys collapse to one entry, which is still valid.
		prev := delta.Snapshot{
			keyFromString(kA): delta.State(sA),
			keyFromString(kB): delta.State(sB),
		}
		next := delta.Snapshot{
			keyFromString(kC): delta.State(sC),
			keyFromString(kD): delta.State(sD),
		}

		d := delta.Compute(prev, next)
		got := delta.Apply(prev, d)

		if !reflect.DeepEqual(got, next) {
			t.Fatalf("round-trip violated\nprev:  %v\nnext:  %v\ndelta: %+v\ngot:   %v",
				prev, next, d, got)
		}
	})
}

// keyFromString splits s on '/' into a Key; missing parts are empty.
func keyFromString(s string) delta.Key {
	parts := strings.SplitN(s, "/", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return delta.Key{Kind: parts[0], Namespace: parts[1], Name: parts[2]}
}
