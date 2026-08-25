package kubeclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoContextKubeconfig = `apiVersion: v1
kind: Config
current-context: prod
contexts:
  - name: prod
    context: {cluster: prod-cluster, user: prod-admin}
  - name: staging
    context: {cluster: staging-cluster, user: staging-dev}
clusters:
  - name: prod-cluster
    cluster: {server: https://prod.example.com}
  - name: staging-cluster
    cluster: {server: https://staging.example.com}
users:
  - name: prod-admin
    user: {token: p}
  - name: staging-dev
    user: {token: s}
`

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(twoContextKubeconfig), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// The whole point of Target is that a user can see which cluster they are
// about to mutate. Defaulting to the current context must be visible, and
// selecting another context must actually change the server.
func TestBuildConfigForContext_DefaultsToCurrentContext(t *testing.T) {
	cfg, target, err := BuildConfigForContext(writeKubeconfig(t), "")
	if err != nil {
		t.Fatalf("BuildConfigForContext: %v", err)
	}
	if target.Context != "prod" {
		t.Errorf("context = %q, want prod", target.Context)
	}
	if cfg.Host != "https://prod.example.com" {
		t.Errorf("host = %q, want the prod server", cfg.Host)
	}
	if target.User != "prod-admin" {
		t.Errorf("user = %q, want prod-admin", target.User)
	}
}

func TestBuildConfigForContext_SelectsNamedContext(t *testing.T) {
	cfg, target, err := BuildConfigForContext(writeKubeconfig(t), "staging")
	if err != nil {
		t.Fatalf("BuildConfigForContext: %v", err)
	}
	if target.Context != "staging" {
		t.Errorf("context = %q, want staging", target.Context)
	}
	if cfg.Host != "https://staging.example.com" {
		t.Errorf("host = %q — --context did not change the target cluster", cfg.Host)
	}
}

// A typo in --context must fail loudly. Silently falling back to the current
// context would apply a rollback to the wrong cluster.
func TestBuildConfigForContext_UnknownContextIsAnError(t *testing.T) {
	_, _, err := BuildConfigForContext(writeKubeconfig(t), "does-not-exist")
	if err == nil {
		t.Fatal("expected an error for an unknown context, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should name the missing context, got: %v", err)
	}
}

func TestTargetString(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Target
		want string
	}{
		{"with context", Target{Source: "~/.kube/config", Context: "prod", Server: "https://p"}, "prod → https://p (~/.kube/config)"},
		{"in-cluster has no context", Target{Source: "in-cluster", Server: "https://10.0.0.1"}, "https://10.0.0.1 (in-cluster)"},
	} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
