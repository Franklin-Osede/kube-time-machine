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

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Target describes the cluster a configuration resolved to. It exists so
// that mutating commands can state, before they act, exactly which cluster
// they are about to change — the failure mode being "rolled back the right
// resource in the wrong cluster", which no confirmation prompt catches if
// the prompt does not say where it is pointing.
type Target struct {
	Source  string // how the config was found: "in-cluster", "--kubeconfig", "$KUBECONFIG", "~/.kube/config"
	Context string // kubeconfig context name; empty when in-cluster
	Server  string // API server URL
	User    string // kubeconfig user name; empty when in-cluster
}

// String renders the target for display above a confirmation prompt.
func (t Target) String() string {
	if t.Context == "" {
		return fmt.Sprintf("%s (%s)", t.Server, t.Source)
	}
	return fmt.Sprintf("%s → %s (%s)", t.Context, t.Server, t.Source)
}

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
	cfg, _, err := BuildConfigForContext(explicit, "")
	return cfg, err
}

// BuildConfigForContext resolves a *rest.Config the same way as BuildConfig
// and additionally reports which cluster it selected, so callers can show it
// before performing a mutation.
//
// contextName selects a named kubeconfig context. It is meaningless
// in-cluster, so a non-empty contextName skips the in-cluster step rather
// than silently ignoring the request.
func BuildConfigForContext(explicit, contextName string) (*rest.Config, Target, error) {
	if explicit != "" {
		return fromFile(explicit, contextName, "--kubeconfig")
	}
	if contextName == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return cfg, Target{Source: "in-cluster", Server: cfg.Host}, nil
		}
	}
	if env := os.Getenv("KUBECONFIG"); env != "" {
		return fromFile(env, contextName, "$KUBECONFIG")
	}
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".kube", "config")
		if _, statErr := os.Stat(path); statErr == nil {
			return fromFile(path, contextName, "~/.kube/config")
		}
	}
	return nil, Target{}, fmt.Errorf("kubeclient: no Kubernetes config found (tried explicit, in-cluster, $KUBECONFIG, $HOME/.kube/config)")
}

// fromFile loads a kubeconfig through the standard loading rules so that
// context overrides and multi-file $KUBECONFIG paths behave exactly as they
// do for kubectl.
func fromFile(path, contextName, source string) (*rest.Config, Target, error) {
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	raw, err := cc.RawConfig()
	if err != nil {
		return nil, Target{}, fmt.Errorf("kubeclient: load kubeconfig %s: %w", path, err)
	}
	selected := contextName
	if selected == "" {
		selected = raw.CurrentContext
	}
	if _, ok := raw.Contexts[selected]; selected != "" && !ok {
		return nil, Target{}, fmt.Errorf("kubeclient: context %q not found in %s", selected, path)
	}

	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, Target{}, fmt.Errorf("kubeclient: build config from %s: %w", path, err)
	}
	t := Target{Source: source, Context: selected, Server: cfg.Host}
	if ctx, ok := raw.Contexts[selected]; ok {
		t.User = ctx.AuthInfo
	}
	return cfg, t, nil
}

// NewClientForContext builds a typed client and reports the cluster it is
// pointed at.
func NewClientForContext(explicit, contextName string) (kubernetes.Interface, Target, error) {
	cfg, target, err := BuildConfigForContext(explicit, contextName)
	if err != nil {
		return nil, Target{}, err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, Target{}, fmt.Errorf("kubeclient: build client: %w", err)
	}
	return client, target, nil
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

// NewDynamicClient runs BuildConfig and returns a dynamic client that can
// work with any Kubernetes resource, including CRDs, without requiring
// generated typed structs. Used by DynamicInformers in Phase 2.
func NewDynamicClient(explicit string) (dynamic.Interface, error) {
	cfg, err := BuildConfig(explicit)
	if err != nil {
		return nil, err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubeclient: build dynamic client: %w", err)
	}
	return dc, nil
}
