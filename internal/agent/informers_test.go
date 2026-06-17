package agent_test

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Franklin-Osede/kube-time-machine/internal/agent"
)

// waitForBufferLen polls buf.Len() until it reaches want or the deadline
// fires. Informer events arrive asynchronously through goroutines, so
// tests need this kind of bounded wait instead of a fixed sleep.
func waitForBufferLen(t *testing.T, buf *agent.Buffer, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if buf.Len() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("buffer never reached len %d (got %d)", want, buf.Len())
}

func deploymentFixture(name, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: name, Image: image}},
				},
			},
		},
	}
}

func configMapFixture(name, val string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Data:       map[string]string{"key": val},
	}
}

// startInformers wires Informers to a buffer and runs Start in a
// goroutine. Returns the context cancel so callers can stop it on
// cleanup.
func startInformers(t *testing.T, client *fake.Clientset, buf *agent.Buffer) context.CancelFunc {
	t.Helper()
	inf := agent.NewInformers(client, buf, 0, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = inf.Start(ctx) }()
	t.Cleanup(cancel)
	return cancel
}

func TestInformers_AddDeploymentPropagatesToBuffer(t *testing.T) {
	client := fake.NewSimpleClientset(deploymentFixture("api", "nginx:1.0"))
	buf := agent.NewBuffer()
	startInformers(t, client, buf)

	waitForBufferLen(t, buf, 1)

	snap := buf.Snapshot()
	got, ok := snap[agent.KeyForDeployment(deploymentFixture("api", "nginx:1.0"))]
	if !ok {
		t.Fatalf("Deployment did not land in buffer: %v", snap)
	}
	if len(got) == 0 {
		t.Errorf("Deployment state is empty")
	}
}

func TestInformers_AddConfigMapPropagatesToBuffer(t *testing.T) {
	client := fake.NewSimpleClientset(configMapFixture("cfg", "v1"))
	buf := agent.NewBuffer()
	startInformers(t, client, buf)

	waitForBufferLen(t, buf, 1)
}

func TestInformers_DeploymentAndConfigMapCoexist(t *testing.T) {
	client := fake.NewSimpleClientset(
		deploymentFixture("api", "nginx:1.0"),
		configMapFixture("cfg", "v1"),
	)
	buf := agent.NewBuffer()
	startInformers(t, client, buf)

	waitForBufferLen(t, buf, 2)
}

func TestInformers_UpdateRefreshesState(t *testing.T) {
	d := deploymentFixture("api", "nginx:1.0")
	client := fake.NewSimpleClientset(d)
	buf := agent.NewBuffer()
	startInformers(t, client, buf)

	waitForBufferLen(t, buf, 1)
	before := buf.Snapshot()[agent.KeyForDeployment(d)]

	d2 := deploymentFixture("api", "nginx:2.0")
	if _, err := client.AppsV1().Deployments("default").Update(context.Background(), d2, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Wait for buffer to reflect the update (the State bytes change).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		after := buf.Snapshot()[agent.KeyForDeployment(d)]
		if string(after) != string(before) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("buffer never reflected the Deployment update")
}

func TestInformers_DeleteRemovesFromBuffer(t *testing.T) {
	d := deploymentFixture("api", "nginx:1.0")
	client := fake.NewSimpleClientset(d)
	buf := agent.NewBuffer()
	startInformers(t, client, buf)

	waitForBufferLen(t, buf, 1)

	if err := client.AppsV1().Deployments("default").Delete(context.Background(), "api", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	waitForBufferLen(t, buf, 0)
}

// TestInformers_ReadyClosesAfterSync pins the readiness signal the
// Snapshotter relies on: Ready() is open before Start and closed once the
// initial cache sync completes.
// TestInformers_ExcludeNamespaceDropsEvents verifies that resources in
// excluded namespaces never reach the buffer.
func TestInformers_ExcludeNamespaceDropsEvents(t *testing.T) {
	// One resource in an excluded namespace, one in a watched namespace.
	excluded := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "system-dep", Namespace: "kube-system"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "nginx"}},
				},
			},
		},
	}
	watched := deploymentFixture("api", "nginx:1.0")
	client := fake.NewSimpleClientset(excluded, watched)
	buf := agent.NewBuffer()

	inf := agent.NewInformers(client, buf, 0, []string{"kube-system"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = inf.Start(ctx) }()

	// Only the watched resource should land in the buffer.
	waitForBufferLen(t, buf, 1)

	snap := buf.Snapshot()
	for k := range snap {
		if k.Namespace == "kube-system" {
			t.Errorf("excluded namespace 'kube-system' leaked into buffer: key=%+v", k)
		}
	}
}

func TestInformers_ReadyClosesAfterSync(t *testing.T) {
	client := fake.NewSimpleClientset(deploymentFixture("api", "nginx:1.0"))
	buf := agent.NewBuffer()
	inf := agent.NewInformers(client, buf, 0, nil)

	select {
	case <-inf.Ready():
		t.Fatal("Ready() was closed before Start()")
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = inf.Start(ctx) }()

	select {
	case <-inf.Ready():
		// closed after sync — correct.
	case <-time.After(2 * time.Second):
		t.Fatal("Ready() never closed after Start() synced")
	}
}

func TestInformers_StartReturnsContextErrorOnCancel(t *testing.T) {
	client := fake.NewSimpleClientset()
	buf := agent.NewBuffer()
	inf := agent.NewInformers(client, buf, 0, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- inf.Start(ctx) }()

	// Give Start time to complete the initial sync.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Start returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return within 1s of cancel")
	}
}
