// Package kubeclient resolves a *rest.Config in the standard
// kubectl/kubelet order and constructs a typed Kubernetes client from
// it. Both the agent and the CLI use this — they MUST agree on how
// configuration is found so user expectations are consistent across
// binaries.
package kubeclient

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// BuildConfig resolves a *rest.Config in this order:
//
//  1. The explicit kubeconfig path, if non-empty.
//  2. In-cluster configuration (succeeds only when running as a pod).
//  3. $KUBECONFIG environment variable.
//  4. $HOME/.kube/config.
//
// The first option to succeed wins. Returns an error only if none of
// the four produces a usable configuration.
func BuildConfig(explicit string) (*rest.Config, error) {
	if explicit != "" {
		return clientcmd.BuildConfigFromFlags("", explicit)
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	if env := os.Getenv("KUBECONFIG"); env != "" {
		return clientcmd.BuildConfigFromFlags("", env)
	}
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".kube", "config")
		if _, statErr := os.Stat(path); statErr == nil {
			return clientcmd.BuildConfigFromFlags("", path)
		}
	}
	return nil, fmt.Errorf("kubeclient: no Kubernetes config found (tried explicit, in-cluster, $KUBECONFIG, $HOME/.kube/config)")
}

// NewClient is a convenience that runs BuildConfig and wraps the result
// in a typed Clientset.
func NewClient(explicit string) (kubernetes.Interface, error) {
	cfg, err := BuildConfig(explicit)
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubeclient: build client: %w", err)
	}
	return client, nil
}
