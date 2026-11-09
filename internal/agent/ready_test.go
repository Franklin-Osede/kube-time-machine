package agent

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

// assertReadyWaitsForBuffer holds the buffer's lock so the event handlers
// block on their first Upsert, as a slow handler does under a large initial
// list. The informer's store syncs regardless; Ready must still stay open
// until the handlers have delivered every listed object, or the first full
// snapshot is taken from a partial buffer.
func assertReadyWaitsForBuffer(t *testing.T, buf *Buffer, ready <-chan struct{}, start func(context.Context) error, want int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buf.mu.Lock()
	go func() { _ = start(ctx) }()
	select {
	case <-ready:
		buf.mu.Unlock()
		t.Fatal("Ready closed while the handlers had not delivered the initial list")
	case <-time.After(500 * time.Millisecond):
	}
	buf.mu.Unlock()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("Ready never closed after the handlers were unblocked")
	}
	if got := buf.Len(); got != want {
		t.Fatalf("buffer has %d entries when Ready closed, want %d", got, want)
	}
}

func TestInformers_ReadyWaitsForHandlers(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "default"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"}},
	)
	buf := NewBuffer()
	inf := NewInformers(client, buf, 0, nil)
	assertReadyWaitsForBuffer(t, buf, inf.Ready(), inf.Start, 2)
}

func TestDynamicInformers_ReadyWaitsForHandlers(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "services"}
	svc := &unstructured.Unstructured{}
	svc.SetAPIVersion("v1")
	svc.SetKind("Service")
	svc.SetNamespace("default")
	svc.SetName("web")
	fc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "ServiceList"}, svc)
	buf := NewBuffer()
	d := NewDynamicInformers(fc, buf, 0, []schema.GroupVersionResource{gvr}, nil)
	assertReadyWaitsForBuffer(t, buf, d.Ready(), d.Start, 1)
}
