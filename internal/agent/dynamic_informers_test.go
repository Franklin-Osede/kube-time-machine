package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

// -- ParseGVR --

func TestParseGVR_ResourceOnly(t *testing.T) {
	gvr, err := ParseGVR("statefulsets")
	if err != nil {
		t.Fatalf("ParseGVR: %v", err)
	}
	want := schema.GroupVersionResource{Resource: "statefulsets"}
	if gvr != want {
		t.Errorf("want %+v, got %+v", want, gvr)
	}
}

func TestParseGVR_ResourceAndGroup(t *testing.T) {
	gvr, err := ParseGVR("statefulsets.apps")
	if err != nil {
		t.Fatalf("ParseGVR: %v", err)
	}
	want := schema.GroupVersionResource{Group: "apps", Resource: "statefulsets"}
	if gvr != want {
		t.Errorf("want %+v, got %+v", want, gvr)
	}
}

func TestParseGVR_FullForm(t *testing.T) {
	gvr, err := ParseGVR("ingresses.networking.k8s.io/v1")
	if err != nil {
		t.Fatalf("ParseGVR: %v", err)
	}
	want := schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	if gvr != want {
		t.Errorf("want %+v, got %+v", want, gvr)
	}
}

func TestParseGVR_EmptyStringErrors(t *testing.T) {
	if _, err := ParseGVR(""); err == nil {
		t.Error("expected error for empty string, got nil")
	}
}

func TestParseGVR_EmptyResourceErrors(t *testing.T) {
	if _, err := ParseGVR(".group/v1"); err == nil {
		t.Error("expected error when resource part is empty, got nil")
	}
}

// -- CombinedReady --

func TestCombinedReady_ClosesBothChannels(t *testing.T) {
	a := make(chan struct{})
	b := make(chan struct{})
	out := CombinedReady(a, b)

	close(a)
	close(b)

	select {
	case <-out:
		// Good: combined channel closed after both inputs closed.
	case <-time.After(time.Second):
		t.Error("CombinedReady did not close after both inputs closed")
	}
}

func TestCombinedReady_WaitsForBoth(t *testing.T) {
	a := make(chan struct{})
	b := make(chan struct{})
	out := CombinedReady(a, b)

	close(a)
	// b is still open — combined channel must not close yet.
	select {
	case <-out:
		t.Error("CombinedReady closed before both inputs were closed")
	default:
	}

	close(b)
	select {
	case <-out:
		// Good.
	case <-time.After(time.Second):
		t.Error("CombinedReady did not close after second input closed")
	}
}

// -- marshalUnstructured --

func TestMarshalUnstructured_ProducesKeyAndStripsRV(t *testing.T) {
	u := &unstructured.Unstructured{}
	u.SetKind("StatefulSet")
	u.SetNamespace("prod")
	u.SetName("redis")
	u.SetResourceVersion("42") // must be stripped by sanitiseUnstructuredMeta

	k, state, err := marshalUnstructured(u)
	if err != nil {
		t.Fatalf("marshalUnstructured: %v", err)
	}
	if k.Kind != "StatefulSet" || k.Namespace != "prod" || k.Name != "redis" {
		t.Errorf("unexpected key: %+v", k)
	}
	if len(state) == 0 {
		t.Error("state must not be empty")
	}
	if strings.Contains(string(state), `"resourceVersion"`) {
		t.Error("state must not contain resourceVersion after sanitisation")
	}
}

func TestMarshalUnstructured_InjectsManagersAnnotation(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "StatefulSet",
		"metadata": map[string]any{
			"name":      "redis",
			"namespace": "prod",
			"managedFields": []any{
				map[string]any{"manager": "helm"},
				map[string]any{"manager": "kubectl"},
			},
		},
	}}

	_, state, err := marshalUnstructured(u)
	if err != nil {
		t.Fatalf("marshalUnstructured: %v", err)
	}
	if !strings.Contains(string(state), `"ktm.io/managers"`) {
		t.Error("state must contain ktm.io/managers annotation")
	}
	if strings.Contains(string(state), `"managedFields"`) {
		t.Error("state must not contain managedFields after sanitisation")
	}
}

// -- handleUpsert namespace exclusion --

func TestDynamicInformers_ExcludeNamespaceFiltersUpserts(t *testing.T) {
	fc := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	buf := NewBuffer()
	d := NewDynamicInformers(fc, buf, 0, nil, []string{"kube-system"})

	excluded := &unstructured.Unstructured{}
	excluded.SetKind("StatefulSet")
	excluded.SetNamespace("kube-system")
	excluded.SetName("coredns")
	d.handleUpsert(excluded, schema.GroupVersionResource{Resource: "statefulsets"}, "add")

	if buf.Len() != 0 {
		t.Errorf("kube-system event should be filtered: got %d entries in buffer", buf.Len())
	}

	included := &unstructured.Unstructured{}
	included.SetKind("StatefulSet")
	included.SetNamespace("default")
	included.SetName("myapp")
	d.handleUpsert(included, schema.GroupVersionResource{Resource: "statefulsets"}, "add")

	if buf.Len() != 1 {
		t.Errorf("default namespace event should be buffered: got %d entries", buf.Len())
	}
}

// -- WithSyncTimeout (P0-2) --
// These tests will fail to compile until WithSyncTimeout is implemented,
// which is the correct RED state.

// TestDynamicInformers_WithSyncTimeoutReturnsReceiver verifies the method exists
// and returns the same *DynamicInformers for fluent chaining.
func TestDynamicInformers_WithSyncTimeoutReturnsReceiver(t *testing.T) {
	fc := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	buf := NewBuffer()
	d := NewDynamicInformers(fc, buf, 0, nil, nil)
	if got := d.WithSyncTimeout(time.Second); got != d {
		t.Error("WithSyncTimeout must return the receiver for chaining")
	}
}

// TestDynamicInformers_SyncTimeoutFiresBeforeParentContextCancel is the key
// behavioural test for P0-2: when informer sync does not complete within the
// configured timeout, Start() returns a descriptive error WITHOUT cancelling
// or waiting for the parent context. This guards against the silent hang when
// --watch-resources names a GVR that doesn't exist in the cluster.
//
// NewSimpleDynamicClientWithCustomListKinds is used instead of
// NewSimpleDynamicClient because the reflector panics at list time when the
// GVR's list kind is absent from the scheme. The custom map registers the
// mapping so the reflector can proceed past that check and reach our
// blocking reactor, which keeps HasSynced() == false until t.Cleanup fires.
func TestDynamicInformers_SyncTimeoutFiresBeforeParentContextCancel(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	gvrToListKind := map[schema.GroupVersionResource]string{gvr: "StatefulSetList"}
	fc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind)

	// Block list calls so the informer's initial list never completes
	// → HasSynced() stays false → WaitForCacheSync times out.
	// The channel is closed in t.Cleanup so the stuck goroutine can exit
	// cleanly (no permanent goroutine leak).
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	fc.Fake.PrependReactor("list", "statefulsets", func(_ ktesting.Action) (bool, runtime.Object, error) {
		<-unblock
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion("apps/v1")
		list.SetKind("StatefulSetList")
		return true, list, nil
	})

	buf := NewBuffer()
	d := NewDynamicInformers(fc, buf, 0, []schema.GroupVersionResource{gvr}, nil).
		WithSyncTimeout(30 * time.Millisecond)

	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	err := d.Start(parentCtx)

	if err == nil {
		t.Fatal("Start must return an error when sync times out")
	}
	// The error must be a sync-specific message, not the parent ctx error.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected sync-specific error (not parent ctx), got: %v", err)
	}
	// Parent context must still be live — the sync timeout must not cancel it.
	select {
	case <-parentCtx.Done():
		t.Error("parent context must not be cancelled by a sync timeout")
	default:
	}
}
