package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestParseGVR_CoreResource(t *testing.T) {
	gvr, err := ParseGVR("services/v1")
	if err != nil {
		t.Fatalf("ParseGVR: %v", err)
	}
	want := schema.GroupVersionResource{Version: "v1", Resource: "services"}
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

func TestParseGVR_RequiresVersion(t *testing.T) {
	for _, input := range []string{"statefulsets", "statefulsets.apps", "statefulsets.apps/", "statefulsets.apps/v1/extra"} {
		if _, err := ParseGVR(input); err == nil {
			t.Errorf("ParseGVR(%q): expected error", input)
		}
	}
}

func TestCombinedReady_ClosesBothChannels(t *testing.T) {
	a := make(chan struct{})
	b := make(chan struct{})
	out := CombinedReady(a, b)

	close(a)
	close(b)

	select {
	case <-out:
	case <-time.After(time.Second):
		t.Error("CombinedReady did not close after both inputs closed")
	}
}

func TestCombinedReady_WaitsForBoth(t *testing.T) {
	a := make(chan struct{})
	b := make(chan struct{})
	out := CombinedReady(a, b)

	close(a)
	// b is still open.
	select {
	case <-out:
		t.Error("CombinedReady closed before both inputs were closed")
	default:
	}

	close(b)
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Error("CombinedReady did not close after second input closed")
	}
}

func TestMarshalUnstructured_ProducesKeyAndStripsRV(t *testing.T) {
	u := &unstructured.Unstructured{}
	u.SetKind("StatefulSet")
	u.SetNamespace("prod")
	u.SetName("redis")
	u.SetResourceVersion("42") // must be stripped

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

// TestDynamicInformers_WithSyncTimeoutReturnsReceiver: it returns its receiver.
func TestDynamicInformers_WithSyncTimeoutReturnsReceiver(t *testing.T) {
	fc := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	buf := NewBuffer()
	d := NewDynamicInformers(fc, buf, 0, nil, nil)
	if got := d.WithSyncTimeout(time.Second); got != d {
		t.Error("WithSyncTimeout must return the receiver for chaining")
	}
}

// TestDynamicInformers_SyncTimeoutFiresBeforeParentContextCancel: when the
// sync times out, Start returns an error without waiting for the parent
// context.
//
// NewSimpleDynamicClient would panic at list time because the GVR's list kind
// is not in the scheme; the custom list kinds let the list reach the blocking
// reactor.
func TestDynamicInformers_SyncTimeoutFiresBeforeParentContextCancel(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	gvrToListKind := map[schema.GroupVersionResource]string{gvr: "StatefulSetList"}
	fc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind)

	// Block list calls so the sync times out; t.Cleanup unblocks them.
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
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected sync-specific error (not parent ctx), got: %v", err)
	}
	// The sync timeout must not cancel the parent context.
	select {
	case <-parentCtx.Done():
		t.Error("parent context must not be cancelled by a sync timeout")
	default:
	}
}

func TestParseGVRList_RefusesTypedKinds(t *testing.T) {
	for _, in := range []string{
		"deployments.apps/v1",
		"configmaps/v1",
		"services/v1, deployments.apps/v1beta2",
	} {
		if _, err := ParseGVRList(in); err == nil {
			t.Errorf("ParseGVRList(%q): want error for a kind the typed informers record", in)
		}
	}
	for _, in := range []string{"secrets/v1", "services/v1,secrets/v1"} {
		if _, err := ParseGVRList(in); err == nil || !strings.Contains(err.Error(), "Secrets are not recorded") {
			t.Errorf("ParseGVRList(%q): err = %v, want Secrets refused", in, err)
		}
	}
	got, err := ParseGVRList("services/v1, statefulsets.apps/v1")
	if err != nil || len(got) != 2 {
		t.Fatalf("ParseGVRList of other kinds: got %v, %v", got, err)
	}
}

// Dynamic objects follow the same sanitisation as typed ones: .status is
// controller-owned and must not reach the snapshot.
func TestMarshalUnstructured_StripsStatus(t *testing.T) {
	obj := func(ready int64) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "StatefulSet",
			"metadata":   map[string]interface{}{"name": "db", "namespace": "default"},
			"spec":       map[string]interface{}{"replicas": int64(1)},
			"status":     map[string]interface{}{"readyReplicas": ready},
		}}
	}
	_, s1, err := marshalUnstructured(obj(0))
	if err != nil {
		t.Fatalf("marshalUnstructured: %v", err)
	}
	_, s2, _ := marshalUnstructured(obj(1))
	if string(s1) != string(s2) || strings.Contains(string(s1), "status") {
		t.Errorf("status leaked into state:\n%s\n%s", s1, s2)
	}
}

func fakeDiscovery(lists ...*metav1.APIResourceList) *discoveryfake.FakeDiscovery {
	return &discoveryfake.FakeDiscovery{Fake: &ktesting.Fake{Resources: lists}}
}

var testAPIs = []*metav1.APIResourceList{
	{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
		{Name: "statefulsets", Kind: "StatefulSet"}, {Name: "statefulsets/scale", Kind: "Scale"},
	}},
	{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "services", Kind: "Service"}}},
	{GroupVersion: "cert-manager.io/v1", APIResources: []metav1.APIResource{{Name: "certificates", Kind: "Certificate"}}},
	{GroupVersion: "cert-manager.io/v1beta1", APIResources: []metav1.APIResource{{Name: "certificates", Kind: "Certificate"}}},
	{GroupVersion: "networking.internal.knative.dev/v1alpha1", APIResources: []metav1.APIResource{{Name: "certificates", Kind: "Certificate"}}},
	{GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{
		{Name: "deployments", Kind: "Deployment"}, {Name: "configs", Kind: "ConfigMap"},
	}},
}

// Keys carry no API group, so a configuration in which two sources write the
// same Kind must stop the agent before it records anything.
func TestCheckDynamicKinds(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string // "" means accepted
	}{
		{"statefulsets.apps/v1, services/v1, certificates.cert-manager.io/v1", ""},
		{"certificates.cert-manager.io/v1, certificates.networking.internal.knative.dev/v1alpha1", `both have Kind "Certificate"`},
		{"certificates.cert-manager.io/v1, certificates.cert-manager.io/v1beta1", `both have Kind "Certificate"`},
		{"deployments.example.io/v1", `apps/v1 (built-in watch) and deployments.example.io/v1 both have Kind "Deployment"`},
		{"configs.example.io/v1", `both have Kind "ConfigMap"`},
		{"widgets.example.io/v1", `does not serve "widgets"`},
		{"widgets.missing.io/v1", "discover"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			gvrs, err := ParseGVRList(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			err = CheckDynamicKinds(fakeDiscovery(testAPIs...), gvrs)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("want accepted, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// A discovery failure must stop startup, not skip the check.
func TestCheckDynamicKinds_DiscoveryErrorStops(t *testing.T) {
	d := fakeDiscovery(testAPIs...)
	d.PrependReactor("*", "*", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	gvrs, _ := ParseGVRList("services/v1")
	if err := CheckDynamicKinds(d, gvrs); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err = %v, want the discovery error", err)
	}
}
