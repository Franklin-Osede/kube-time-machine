package kubeclient_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Franklin-Osede/kube-time-machine/internal/kubeclient"
)

// writeKubeconfig writes a minimal kubeconfig at path pointing at serverURL.
// A distinct URL per source shows which one BuildConfig picked.
func writeKubeconfig(t *testing.T, path, serverURL string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for kubeconfig: %v", err)
	}
	content := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
`, serverURL)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
}

// TestBuildConfig_PrecedenceTable covers explicit > $KUBECONFIG >
// $HOME/.kube/config and the error path. In-cluster is not covered: outside a
// pod rest.InClusterConfig fails and BuildConfig falls through.
func TestBuildConfig_PrecedenceTable(t *testing.T) {
	cases := []struct {
		name     string
		prepare  func(t *testing.T, tmp string) (explicit string)
		wantHost string
		wantErr  bool
	}{
		{
			name: "explicit path wins even when env and home are set",
			prepare: func(t *testing.T, tmp string) string {
				explicitPath := filepath.Join(tmp, "explicit.yaml")
				envPath := filepath.Join(tmp, "env.yaml")
				homePath := filepath.Join(tmp, ".kube", "config")
				writeKubeconfig(t, explicitPath, "https://explicit:6443")
				writeKubeconfig(t, envPath, "https://env:6443")
				writeKubeconfig(t, homePath, "https://home:6443")
				t.Setenv("KUBECONFIG", envPath)
				t.Setenv("HOME", tmp)
				return explicitPath
			},
			wantHost: "https://explicit:6443",
		},
		{
			name: "$KUBECONFIG used when explicit is empty",
			prepare: func(t *testing.T, tmp string) string {
				envPath := filepath.Join(tmp, "env.yaml")
				homePath := filepath.Join(tmp, ".kube", "config")
				writeKubeconfig(t, envPath, "https://env:6443")
				writeKubeconfig(t, homePath, "https://home:6443")
				t.Setenv("KUBECONFIG", envPath)
				t.Setenv("HOME", tmp)
				return ""
			},
			wantHost: "https://env:6443",
		},
		{
			name: "$HOME/.kube/config used when explicit and $KUBECONFIG are empty",
			prepare: func(t *testing.T, tmp string) string {
				homePath := filepath.Join(tmp, ".kube", "config")
				writeKubeconfig(t, homePath, "https://home:6443")
				t.Setenv("KUBECONFIG", "")
				t.Setenv("HOME", tmp)
				return ""
			},
			wantHost: "https://home:6443",
		},
		{
			name: "error when nothing is available",
			prepare: func(t *testing.T, tmp string) string {
				t.Setenv("KUBECONFIG", "")
				t.Setenv("HOME", tmp)
				return ""
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			explicit := tc.prepare(t, tmp)

			cfg, err := kubeclient.BuildConfig(explicit)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg == nil {
				t.Fatal("nil config returned without error")
			}
			if cfg.Host != tc.wantHost {
				t.Errorf("host: want %q, got %q", tc.wantHost, cfg.Host)
			}
		})
	}
}

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

func writeTwoContextKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(twoContextKubeconfig), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// The default context must be reported, and --context must change the
// server.
func TestBuildConfigForContext_DefaultsToCurrentContext(t *testing.T) {
	cfg, target, err := kubeclient.BuildConfigForContext(writeTwoContextKubeconfig(t), "")
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
	cfg, target, err := kubeclient.BuildConfigForContext(writeTwoContextKubeconfig(t), "staging")
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

// A typo in --context must fail, not fall back to the current context.
func TestBuildConfigForContext_UnknownContextIsAnError(t *testing.T) {
	_, _, err := kubeclient.BuildConfigForContext(writeTwoContextKubeconfig(t), "does-not-exist")
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
		in   kubeclient.Target
		want string
	}{
		{"with context", kubeclient.Target{Source: "~/.kube/config", Context: "prod", Server: "https://p"}, "prod → https://p (~/.kube/config)"},
		{"in-cluster has no context", kubeclient.Target{Source: "in-cluster", Server: "https://10.0.0.1"}, "https://10.0.0.1 (in-cluster)"},
	} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
