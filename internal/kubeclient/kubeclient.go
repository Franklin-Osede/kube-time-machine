// Package kubeclient resolves a *rest.Config and builds Kubernetes clients.
// The agent and the CLI both use it so they find configuration the same way.
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

// Target describes the cluster a configuration resolved to, so a mutating
// command can say which cluster it is about to change before it acts.
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

// BuildConfig resolves a *rest.Config from, in order: the explicit
// kubeconfig path, in-cluster configuration, $KUBECONFIG, and
// $HOME/.kube/config. In-cluster falls through when it fails; the first
// kubeconfig source that is set is used, and an error loading it is returned
// rather than trying the next.
func BuildConfig(explicit string) (*rest.Config, error) {
	cfg, _, err := BuildConfigForContext(explicit, "")
	return cfg, err
}

// BuildConfigForContext resolves a *rest.Config like BuildConfig and reports
// the cluster it selected. A non-empty contextName skips the in-cluster step,
// where a context is meaningless, rather than silently ignoring it.
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

// fromFile loads a kubeconfig through clientcmd's loading rules, so context
// overrides behave as they do for kubectl.
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

// NewClient builds a typed client from BuildConfig.
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

// NewDynamicClient builds a dynamic client from BuildConfig.
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
