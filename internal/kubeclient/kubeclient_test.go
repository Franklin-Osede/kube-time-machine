package kubeclient_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Franklin-Osede/kube-time-machine/internal/kubeclient"
)

// writeKubeconfig writes a minimal valid kubeconfig at path pointing at
// serverURL. The serverURL is what later assertions read back via
// rest.Config.Host to confirm WHICH source BuildConfig picked. Distinct
// URLs per source make the precedence visible without parsing files.
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

// TestBuildConfig_PrecedenceTable covers the three resolvable sources
// BuildConfig considers — explicit > $KUBECONFIG > $HOME/.kube/config —
// plus the "nothing works" error path. In-cluster (the second source
// in the real order, between explicit and $KUBECONFIG) is NOT covered
// here: rest.InClusterConfig() requires /var/run/secrets/... to exist,
// which doesn't on a laptop or a normal GHA runner. In those
// environments InClusterConfig() returns an error and the production
// code falls through to the next source — exactly the behaviour the
// remaining cases rely on. Verifying in-cluster itself would require a
// real Pod fixture and is deferred.
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
				// tmp has no .kube/config; no env var; no explicit.
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
