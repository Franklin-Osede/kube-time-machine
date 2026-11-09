package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/Franklin-Osede/kube-time-machine/internal/agent"
	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
)

// makeDeployment builds a typed Deployment at resourceVersion rv.
func makeDeployment(name, image, rv string) *appsv1.Deployment {
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       "default",
			ResourceVersion: rv,
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: image}},
				},
			},
		},
	}
}

func deploymentSnapshotPayload(t *testing.T, name, image string) []byte {
	t.Helper()
	// Sanitised payloads carry no RV.
	d := makeDeployment(name, image, "")
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestRollbackDeployment_HappyPath_UsesPreviewRV(t *testing.T) {
	liveDep := makeDeployment("api", "nginx:1.27", "100")
	client := fake.NewSimpleClientset(liveDep)

	target := key("Deployment", "default", "api")
	payload := deploymentSnapshotPayload(t, "api", "nginx:1.25")

	var out bytes.Buffer
	if err := rollbackDeployment(
		context.Background(), &out, strings.NewReader("y\n"),
		client, target, payload, false, false,
	); err != nil {
		t.Fatalf("rollbackDeployment: %v", err)
	}

	got, err := client.AppsV1().Deployments("default").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("post-rollback Get: %v", err)
	}
	if got.Spec.Template.Spec.Containers[0].Image != "nginx:1.25" {
		t.Errorf("cluster image after rollback: want nginx:1.25, got %q", got.Spec.Template.Spec.Containers[0].Image)
	}
	if !strings.Contains(out.String(), "rollback applied") {
		t.Errorf("expected success message, got:\n%s", out.String())
	}
}

func TestRollbackDeployment_AutoYesBypassesPrompt(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("api", "nginx:1.27", "100"))
	payload := deploymentSnapshotPayload(t, "api", "nginx:1.25")

	var out bytes.Buffer
	if err := rollbackDeployment(
		context.Background(), &out, strings.NewReader(""),
		client, key("Deployment", "default", "api"), payload, true, false,
	); err != nil {
		t.Fatalf("rollbackDeployment: %v", err)
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Errorf("--yes should skip the prompt, but [y/N] appeared in output:\n%s", out.String())
	}
}

func TestRollbackDeployment_AbortedAtPrompt(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("api", "nginx:1.27", "100"))
	payload := deploymentSnapshotPayload(t, "api", "nginx:1.25")

	var out bytes.Buffer
	if err := rollbackDeployment(
		context.Background(), &out, strings.NewReader("n\n"),
		client, key("Deployment", "default", "api"), payload, false, false,
	); err != nil {
		t.Fatalf("rollbackDeployment: %v", err)
	}
	got, _ := client.AppsV1().Deployments("default").Get(context.Background(), "api", metav1.GetOptions{})
	if got.Spec.Template.Spec.Containers[0].Image != "nginx:1.27" {
		t.Errorf("cluster should be untouched after abort; got %q", got.Spec.Template.Spec.Containers[0].Image)
	}
	if !strings.Contains(out.String(), "rollback aborted") {
		t.Errorf("expected 'rollback aborted', got:\n%s", out.String())
	}
}

// TestRollbackDeployment_ConflictReturnsActionableError: a 409 on Update is
// reported with a re-run instruction, never retried (ADR-0006).
func TestRollbackDeployment_ConflictReturnsActionableError(t *testing.T) {
	live := makeDeployment("api", "nginx:1.27", "100")
	client := fake.NewSimpleClientset(live)

	// The API server answers a stale resourceVersion with 409 Conflict.
	client.PrependReactor("update", "deployments", func(_ ktesting.Action) (bool, runtime.Object, error) {
		gvr := schema.GroupResource{Group: "apps", Resource: "deployments"}
		return true, nil, apierrors.NewConflict(gvr, "api",
			fmt.Errorf("the object has been modified; please apply your changes to the latest version"))
	})

	payload := deploymentSnapshotPayload(t, "api", "nginx:1.25")

	var out bytes.Buffer
	err := rollbackDeployment(
		context.Background(), &out, strings.NewReader("y\n"),
		client, key("Deployment", "default", "api"), payload, false, false,
	)
	if err == nil {
		t.Fatal("expected an error on 409 Conflict, got nil")
	}

	// The message must name the rejection, the cause and the next step.
	msg := err.Error()
	for _, want := range []string{
		"rollback rejected",
		"changed in the cluster",
		"Re-run `ktm rollback`",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("conflict error missing %q; full message: %s", want, msg)
		}
	}

	got, _ := client.AppsV1().Deployments("default").Get(context.Background(), "api", metav1.GetOptions{})
	if got.Spec.Template.Spec.Containers[0].Image != "nginx:1.27" {
		t.Errorf("cluster image after rejected rollback: want nginx:1.27, got %q",
			got.Spec.Template.Spec.Containers[0].Image)
	}
}

// TestRollbackDeployment_StripsServerOwnedFieldsOnUpdate: snapshots keep UID
// and CreationTimestamp, so Update must strip them; a UID from before a
// delete-and-recreate is rejected as an immutable-field change.
func TestRollbackDeployment_StripsServerOwnedFieldsOnUpdate(t *testing.T) {
	// Live UID after a delete-and-recreate.
	live := makeDeployment("api", "nginx:1.27", "100")
	live.UID = "live-uid-xyz"
	client := fake.NewSimpleClientset(live)

	// The snapshot carries the old UID.
	snapshotDep := makeDeployment("api", "nginx:1.25", "")
	snapshotDep.UID = "snapshot-uid-abc"
	snapshotDep.CreationTimestamp = metav1.Now()
	payload, _ := json.Marshal(snapshotDep)

	var capturedUpdate *appsv1.Deployment
	client.PrependReactor("update", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		if ua, ok := a.(ktesting.UpdateAction); ok {
			if obj, ok := ua.GetObject().(*appsv1.Deployment); ok {
				capturedUpdate = obj.DeepCopy()
			}
		}
		return false, nil, nil
	})

	var out bytes.Buffer
	if err := rollbackDeployment(
		context.Background(), &out, strings.NewReader("y\n"),
		client, key("Deployment", "default", "api"), payload, false, false,
	); err != nil {
		t.Fatalf("rollbackDeployment: %v", err)
	}

	if capturedUpdate == nil {
		t.Fatal("expected an Update call, none captured")
	}
	if capturedUpdate.UID != "" {
		t.Errorf("UID not stripped before Update: got %q, want empty. A real API server would reject this with an immutable-field error if the live UID differs from the payload UID.", capturedUpdate.UID)
	}
	if !capturedUpdate.CreationTimestamp.IsZero() {
		t.Errorf("CreationTimestamp not stripped before Update: got %v, want zero", capturedUpdate.CreationTimestamp)
	}
	// The RV read at preview time (ADR-0006), not empty and not the snapshot's.
	if capturedUpdate.ResourceVersion != "100" {
		t.Errorf("ResourceVersion on Update: want live %q, got %q", "100", capturedUpdate.ResourceVersion)
	}
}

// TestRollbackConfigMap_StripsServerOwnedFieldsOnUpdate is the ConfigMap
// counterpart.
func TestRollbackConfigMap_StripsServerOwnedFieldsOnUpdate(t *testing.T) {
	live := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cfg", Namespace: "default", ResourceVersion: "55", UID: "live-uid",
		},
		Data: map[string]string{"env": "prod"},
	}
	client := fake.NewSimpleClientset(live)

	snapshotCM := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "cfg", Namespace: "default", UID: "snapshot-uid", CreationTimestamp: metav1.Now(),
		},
		Data: map[string]string{"env": "dev"},
	}
	payload, _ := json.Marshal(snapshotCM)

	var capturedUpdate *corev1.ConfigMap
	client.PrependReactor("update", "configmaps", func(a ktesting.Action) (bool, runtime.Object, error) {
		if ua, ok := a.(ktesting.UpdateAction); ok {
			if obj, ok := ua.GetObject().(*corev1.ConfigMap); ok {
				capturedUpdate = obj.DeepCopy()
			}
		}
		return false, nil, nil
	})

	var out bytes.Buffer
	if err := rollbackConfigMap(
		context.Background(), &out, strings.NewReader("y\n"),
		client, key("ConfigMap", "default", "cfg"), payload, false, false,
	); err != nil {
		t.Fatalf("rollbackConfigMap: %v", err)
	}

	if capturedUpdate == nil {
		t.Fatal("expected an Update call, none captured")
	}
	if capturedUpdate.UID != "" {
		t.Errorf("UID not stripped before Update on ConfigMap: got %q", capturedUpdate.UID)
	}
	if capturedUpdate.ResourceVersion != "55" {
		t.Errorf("ResourceVersion on Update: want live %q, got %q", "55", capturedUpdate.ResourceVersion)
	}
}

// TestRollbackDeployment_PreviewIsSanitised: server-owned churn
// (resourceVersion, managedFields, status) never appears in the preview.
func TestRollbackDeployment_PreviewIsSanitised(t *testing.T) {
	live := makeDeployment("api", "nginx:1.27", "100")
	live.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kubectl"}}
	live.Status = appsv1.DeploymentStatus{ObservedGeneration: 7, Replicas: 3}
	client := fake.NewSimpleClientset(live)

	_, payload, err := agent.MarshalDeployment(makeDeployment("api", "nginx:1.25", ""))
	if err != nil {
		t.Fatalf("MarshalDeployment: %v", err)
	}

	var out bytes.Buffer
	// Decline at the prompt.
	if err := rollbackDeployment(
		context.Background(), &out, strings.NewReader("n\n"),
		client, key("Deployment", "default", "api"), []byte(payload), false, false,
	); err != nil {
		t.Fatalf("rollbackDeployment: %v", err)
	}

	preview := out.String()
	if !strings.Contains(preview, "nginx:1.25") {
		t.Errorf("preview should show the rollback target image; got:\n%s", preview)
	}
	for _, noise := range []string{"resourceVersion", "managedFields", "observedGeneration"} {
		if strings.Contains(preview, noise) {
			t.Errorf("preview leaked server-owned field %q (unsanitised preview):\n%s", noise, preview)
		}
	}
}

func TestRollbackDeployment_404PathCreates(t *testing.T) {
	client := fake.NewSimpleClientset() // empty cluster
	payload := deploymentSnapshotPayload(t, "api", "nginx:1.25")

	var out bytes.Buffer
	if err := rollbackDeployment(
		context.Background(), &out, strings.NewReader("y\n"),
		client, key("Deployment", "default", "api"), payload, false, true,
	); err != nil {
		t.Fatalf("rollbackDeployment: %v", err)
	}
	if !strings.Contains(out.String(), "will be CREATED") {
		t.Errorf("expected 'will be CREATED' notice, got:\n%s", out.String())
	}
	got, err := client.AppsV1().Deployments("default").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("post-create Get: %v", err)
	}
	if got.Spec.Template.Spec.Containers[0].Image != "nginx:1.25" {
		t.Errorf("created image: want nginx:1.25, got %q", got.Spec.Template.Spec.Containers[0].Image)
	}
	// stripServerOwned should have wiped these on the inbound payload.
	if got.ResourceVersion == "100" {
		t.Errorf("ResourceVersion should not be persisted from payload on Create")
	}
}

func TestRollbackConfigMap_HappyPath(t *testing.T) {
	live := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default", ResourceVersion: "55"},
		Data:       map[string]string{"env": "prod"},
	}
	client := fake.NewSimpleClientset(live)

	targetCM := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Data:       map[string]string{"env": "dev"},
	}
	payload, _ := json.Marshal(targetCM)

	var out bytes.Buffer
	if err := rollbackConfigMap(
		context.Background(), &out, strings.NewReader("y\n"),
		client, key("ConfigMap", "default", "cfg"), payload, false, false,
	); err != nil {
		t.Fatalf("rollbackConfigMap: %v", err)
	}
	got, _ := client.CoreV1().ConfigMaps("default").Get(context.Background(), "cfg", metav1.GetOptions{})
	if got.Data["env"] != "dev" {
		t.Errorf("ConfigMap data after rollback: want env=dev, got %q", got.Data["env"])
	}
}

func TestRunRollback_UnknownKindErrors(t *testing.T) {
	// runRollback builds its client from a kubeconfig, so this asserts only
	// the message format, not the dispatch.
	err := fmt.Errorf("rollback supports only Deployment and ConfigMap in MVP (got %q)", "Service")
	if !strings.Contains(err.Error(), "Deployment and ConfigMap") {
		t.Errorf("unexpected error format: %v", err)
	}
}

func TestStripServerOwned_RemovesEverything(t *testing.T) {
	m := metav1.ObjectMeta{
		Name:              "x",
		ResourceVersion:   "100",
		UID:               "abc-123",
		CreationTimestamp: metav1.Now(),
		Generation:        7,
		ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "k"}},
		SelfLink:          "/api/v1/x",
		DeletionTimestamp: &metav1.Time{},
		DeletionGracePeriodSeconds: func() *int64 {
			v := int64(30)
			return &v
		}(),
	}
	stripServerOwned(&m)
	if m.DeletionTimestamp != nil || m.DeletionGracePeriodSeconds != nil {
		t.Errorf("deletion fields not stripped: %+v", m)
	}

	if m.Name != "x" {
		t.Errorf("Name should be preserved")
	}
	if m.ResourceVersion != "" || m.UID != "" || m.Generation != 0 || m.SelfLink != "" || m.ManagedFields != nil {
		t.Errorf("server-owned fields not fully stripped: %+v", m)
	}
	if !m.CreationTimestamp.IsZero() {
		t.Errorf("CreationTimestamp should be zero, got %v", m.CreationTimestamp)
	}
}

func TestPromptConfirm(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"Y\n", true},
		{"n\n", false},
		{"\n", false},
		{"maybe\n", false},
	} {
		t.Run(strings.TrimSpace(tc.input)+"_"+boolStr(tc.want), func(t *testing.T) {
			var out bytes.Buffer
			got := promptConfirm(strings.NewReader(tc.input), &out)
			if got != tc.want {
				t.Errorf("input %q: want %v, got %v", tc.input, tc.want, got)
			}
		})
	}
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// A deleted resource is recreated only with --allow-create.
func TestRollbackDeployment_404WithoutAllowCreateRefuses(t *testing.T) {
	client := fake.NewSimpleClientset() // empty cluster — every Get is a 404
	payload := []byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"default"},"spec":{"replicas":3}}`)

	var out bytes.Buffer
	err := rollbackDeployment(
		context.Background(), &out, strings.NewReader("y\n"),
		client, key("Deployment", "default", "api"), payload, true, false,
	)
	if err == nil {
		t.Fatal("expected refusal when the resource is absent and --allow-create is not set")
	}
	if !strings.Contains(err.Error(), "--allow-create") {
		t.Errorf("error should point at the opt-in flag, got: %v", err)
	}
	if acts := client.Actions(); len(acts) != 1 || acts[0].GetVerb() != "get" {
		t.Errorf("expected a single get and no create, got %v", acts)
	}
}

func TestRollbackConfigMap_404WithoutAllowCreateRefuses(t *testing.T) {
	client := fake.NewSimpleClientset()
	payload := []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"cfg","namespace":"default"},"data":{"k":"v"}}`)

	var out bytes.Buffer
	err := rollbackConfigMap(
		context.Background(), &out, strings.NewReader("y\n"),
		client, key("ConfigMap", "default", "cfg"), payload, true, false,
	)
	if err == nil {
		t.Fatal("expected refusal when the resource is absent and --allow-create is not set")
	}
	if !strings.Contains(err.Error(), "--allow-create") {
		t.Errorf("error should point at the opt-in flag, got: %v", err)
	}
}

// Annotations the recorder never stores, as a live object managed by Helm,
// Argo CD and client-side kubectl apply carries them.
var unrecordedLive = map[string]string{
	"meta.helm.sh/release-name":                        "ktm",
	"argocd.argoproj.io/sync-options":                  "Prune=false",
	"kubectl.kubernetes.io/last-applied-configuration": `{"kind":"Deployment"}`,
}

// assertWrittenMeta checks the annotations of the object rollback wrote:
// unrecorded live annotations survive, the snapshot's own annotations are
// restored, and no ktm.io/ annotation reaches the cluster.
func assertWrittenMeta(t *testing.T, got map[string]string, wantTeam string) {
	t.Helper()
	for k, v := range unrecordedLive {
		if got[k] != v {
			t.Errorf("annotation %s = %q, want live value %q kept", k, got[k], v)
		}
	}
	if got["team"] != wantTeam {
		t.Errorf("annotation team = %q, want %q from the snapshot", got["team"], wantTeam)
	}
	for k := range got {
		if strings.HasPrefix(k, "ktm.io/") {
			t.Errorf("rollback wrote KTM's own annotation %s to the cluster", k)
		}
	}
}

// assertPreviewMatchesWrite checks that the preview shows the snapshot's
// change and nothing that the write does not do: no ktm.io/ annotation and
// no unrecorded annotation, which the write leaves untouched.
func assertPreviewMatchesWrite(t *testing.T, preview string, wantChanged ...string) {
	t.Helper()
	for _, want := range wantChanged {
		if !strings.Contains(preview, want) {
			t.Errorf("preview should show %q:\n%s", want, preview)
		}
	}
	for _, hidden := range []string{"ktm.io/", "meta.helm.sh", "argocd.argoproj.io", "last-applied-configuration"} {
		if strings.Contains(preview, hidden) {
			t.Errorf("preview shows %q, which the write does not change:\n%s", hidden, preview)
		}
	}
}

func withAnnotations(base map[string]string, extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestRollbackDeployment_UpdateKeepsUnrecordedAnnotations(t *testing.T) {
	live := makeDeployment("api", "nginx:1.27", "100")
	live.Annotations = withAnnotations(unrecordedLive, map[string]string{"team": "b"})
	live.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kubectl"}}
	client := fake.NewSimpleClientset(live)

	old := makeDeployment("api", "nginx:1.25", "")
	old.Annotations = map[string]string{"team": "a"}
	old.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "helm"}}
	_, payload, err := agent.MarshalDeployment(old)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := rollbackDeployment(context.Background(), &out, strings.NewReader(""),
		client, key("Deployment", "default", "api"), payload, true, false); err != nil {
		t.Fatalf("rollbackDeployment: %v", err)
	}
	assertPreviewMatchesWrite(t, out.String(), "nginx:1.25", `"team": "a"`)

	got, err := client.AppsV1().Deployments("default").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if img := got.Spec.Template.Spec.Containers[0].Image; img != "nginx:1.25" {
		t.Errorf("image = %q, want nginx:1.25", img)
	}
	assertWrittenMeta(t, got.Annotations, "a")
}

func TestRollbackConfigMap_UpdateKeepsUnrecordedAnnotations(t *testing.T) {
	live := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cfg", Namespace: "default", ResourceVersion: "55",
			Annotations:   withAnnotations(unrecordedLive, map[string]string{"team": "b"}),
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
		},
		Data: map[string]string{"env": "prod"},
	}
	client := fake.NewSimpleClientset(live)

	_, payload, err := agent.MarshalConfigMap(&corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "cfg", Namespace: "default",
			Annotations:   map[string]string{"team": "a"},
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "helm"}},
		},
		Data: map[string]string{"env": "dev"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := rollbackConfigMap(context.Background(), &out, strings.NewReader(""),
		client, key("ConfigMap", "default", "cfg"), payload, true, false); err != nil {
		t.Fatalf("rollbackConfigMap: %v", err)
	}
	assertPreviewMatchesWrite(t, out.String(), `"env": "dev"`, `"team": "a"`)

	got, err := client.CoreV1().ConfigMaps("default").Get(context.Background(), "cfg", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Data["env"] != "dev" {
		t.Errorf("data.env = %q, want dev", got.Data["env"])
	}
	assertWrittenMeta(t, got.Annotations, "a")
}

func TestRollbackCreate_WritesNoKTMAnnotations(t *testing.T) {
	ctx := context.Background()
	managed := []metav1.ManagedFieldsEntry{{Manager: "helm"}}

	t.Run("Deployment", func(t *testing.T) {
		old := makeDeployment("api", "nginx:1.25", "")
		old.Annotations = map[string]string{"team": "a"}
		old.ManagedFields = managed
		_, payload, err := agent.MarshalDeployment(old)
		if err != nil {
			t.Fatal(err)
		}
		client := fake.NewSimpleClientset()
		var out bytes.Buffer
		if err := rollbackDeployment(ctx, &out, strings.NewReader(""),
			client, key("Deployment", "default", "api"), payload, true, true); err != nil {
			t.Fatalf("rollbackDeployment: %v", err)
		}
		got, err := client.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got.Annotations["team"] != "a" || len(got.Annotations) != 1 {
			t.Errorf("annotations = %v, want only team=a", got.Annotations)
		}
		if strings.Contains(out.String(), "ktm.io/") {
			t.Errorf("create preview shows a ktm.io/ annotation:\n%s", out.String())
		}
	})

	t.Run("ConfigMap", func(t *testing.T) {
		_, payload, err := agent.MarshalConfigMap(&corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "cfg", Namespace: "default",
				Annotations: map[string]string{"team": "a"}, ManagedFields: managed,
			},
			Data: map[string]string{"env": "dev"},
		})
		if err != nil {
			t.Fatal(err)
		}
		client := fake.NewSimpleClientset()
		var out bytes.Buffer
		if err := rollbackConfigMap(ctx, &out, strings.NewReader(""),
			client, key("ConfigMap", "default", "cfg"), payload, true, true); err != nil {
			t.Fatalf("rollbackConfigMap: %v", err)
		}
		got, err := client.CoreV1().ConfigMaps("default").Get(ctx, "cfg", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got.Annotations["team"] != "a" || len(got.Annotations) != 1 {
			t.Errorf("annotations = %v, want only team=a", got.Annotations)
		}
		if strings.Contains(out.String(), "ktm.io/") {
			t.Errorf("create preview shows a ktm.io/ annotation:\n%s", out.String())
		}
	})
}

// previewOf renders what showPreviewAndConfirm prints for before -> after.
func previewOf(target delta.Key, before, after delta.State) string {
	var buf bytes.Buffer
	printDelta(&buf, delta.Delta{Modified: map[delta.Key]delta.State{target: after}}, delta.Snapshot{target: before})
	return buf.String()
}

// The preview must be exactly the diff between the live object and what the
// Update sends, even for a payload carrying an unrecorded annotation that
// live does not have.
func TestRollback_PreviewIsTheDiffOfWhatIsWritten(t *testing.T) {
	ctx := context.Background()

	t.Run("Deployment", func(t *testing.T) {
		live := makeDeployment("api", "nginx:1.27", "100")
		live.Annotations = withAnnotations(unrecordedLive, map[string]string{"team": "b"})
		client := fake.NewSimpleClientset(live)
		var written *appsv1.Deployment
		client.PrependReactor("update", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
			written = a.(ktesting.UpdateAction).GetObject().(*appsv1.Deployment).DeepCopy()
			return false, nil, nil
		})

		old := makeDeployment("api", "nginx:1.25", "")
		old.Annotations = map[string]string{"team": "a", "meta.helm.sh/stale": "old"}
		payload, err := json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer
		target := key("Deployment", "default", "api")
		if err := rollbackDeployment(ctx, &out, strings.NewReader(""), client, target, payload, true, false); err != nil {
			t.Fatalf("rollbackDeployment: %v", err)
		}
		if written == nil {
			t.Fatal("no Update was sent")
		}
		if _, ok := written.Annotations["meta.helm.sh/stale"]; ok {
			t.Error("wrote an unrecorded annotation from the payload that live does not have")
		}

		liveView, sent := live.DeepCopy(), written.DeepCopy()
		stripServerOwned(&liveView.ObjectMeta)
		stripServerOwned(&sent.ObjectMeta)
		_, before, _ := agent.MarshalDeployment(liveView)
		_, after, _ := agent.MarshalDeployment(sent)
		if want := previewOf(target, before, after); !strings.Contains(out.String(), want) {
			t.Errorf("preview is not the diff of live against the written object.\nwant:\n%s\ngot:\n%s", want, out.String())
		}
	})

	t.Run("ConfigMap", func(t *testing.T) {
		live := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cfg", Namespace: "default", ResourceVersion: "55",
				Annotations: withAnnotations(unrecordedLive, map[string]string{"team": "b"}),
			},
			Data: map[string]string{"env": "prod"},
		}
		client := fake.NewSimpleClientset(live)
		var written *corev1.ConfigMap
		client.PrependReactor("update", "configmaps", func(a ktesting.Action) (bool, runtime.Object, error) {
			written = a.(ktesting.UpdateAction).GetObject().(*corev1.ConfigMap).DeepCopy()
			return false, nil, nil
		})

		payload, err := json.Marshal(&corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "cfg", Namespace: "default",
				Annotations: map[string]string{"team": "a", "meta.helm.sh/stale": "old"},
			},
			Data: map[string]string{"env": "dev"},
		})
		if err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer
		target := key("ConfigMap", "default", "cfg")
		if err := rollbackConfigMap(ctx, &out, strings.NewReader(""), client, target, payload, true, false); err != nil {
			t.Fatalf("rollbackConfigMap: %v", err)
		}
		if written == nil {
			t.Fatal("no Update was sent")
		}
		if _, ok := written.Annotations["meta.helm.sh/stale"]; ok {
			t.Error("wrote an unrecorded annotation from the payload that live does not have")
		}

		liveView, sent := live.DeepCopy(), written.DeepCopy()
		stripServerOwned(&liveView.ObjectMeta)
		stripServerOwned(&sent.ObjectMeta)
		_, before, _ := agent.MarshalConfigMap(liveView)
		_, after, _ := agent.MarshalConfigMap(sent)
		if want := previewOf(target, before, after); !strings.Contains(out.String(), want) {
			t.Errorf("preview is not the diff of live against the written object.\nwant:\n%s\ngot:\n%s", want, out.String())
		}
	})
}

// ADR-0006: exactly one Get, before the prompt. A static fake returns the
// same RV either way, so count the Gets.
func TestRollback_SingleGetBeforeUpdate(t *testing.T) {
	ctx := context.Background()
	countGets := func(client *fake.Clientset, resource string) *int {
		n := 0
		client.PrependReactor("get", resource, func(ktesting.Action) (bool, runtime.Object, error) {
			n++
			return false, nil, nil
		})
		return &n
	}

	t.Run("Deployment", func(t *testing.T) {
		client := fake.NewSimpleClientset(makeDeployment("api", "nginx:1.27", "100"))
		gets := countGets(client, "deployments")
		if err := rollbackDeployment(ctx, &bytes.Buffer{}, strings.NewReader("y\n"), client,
			key("Deployment", "default", "api"), deploymentSnapshotPayload(t, "api", "nginx:1.25"), false, false); err != nil {
			t.Fatalf("rollbackDeployment: %v", err)
		}
		if *gets != 1 {
			t.Errorf("rollback made %d Gets, want exactly 1", *gets)
		}
	})

	t.Run("ConfigMap", func(t *testing.T) {
		client := fake.NewSimpleClientset(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default", ResourceVersion: "55"},
			Data:       map[string]string{"env": "prod"},
		})
		gets := countGets(client, "configmaps")
		payload, _ := json.Marshal(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
			Data:       map[string]string{"env": "dev"},
		})
		if err := rollbackConfigMap(ctx, &bytes.Buffer{}, strings.NewReader("y\n"), client,
			key("ConfigMap", "default", "cfg"), payload, false, false); err != nil {
			t.Fatalf("rollbackConfigMap: %v", err)
		}
		if *gets != 1 {
			t.Errorf("rollback made %d Gets, want exactly 1", *gets)
		}
	})
}
