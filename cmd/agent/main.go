// Command ktm-agent runs inside a Kubernetes cluster, watches
// Deployments and ConfigMaps via client-go informers, and persists
// periodic snapshots (full + deltas) to a local PersistentVolume.
//
// Configuration flags:
//
//	--kubeconfig    explicit path; if empty, falls back to in-cluster,
//	                then to $KUBECONFIG, then to $HOME/.kube/config
//	--storage-dir   directory where snapshots are persisted
//	--interval      how often to flush snapshots
//	--full-every    take a full reference snapshot every N flushes
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"os/signal"

	"github.com/Franklin-Osede/kube-time-machine/internal/agent"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("ktm-agent: fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		kubeconfig = flag.String("kubeconfig", "", "path to kubeconfig (empty = try in-cluster, fall back to $KUBECONFIG, then $HOME/.kube/config)")
		storageDir = flag.String("storage-dir", "/var/lib/ktm", "directory where snapshots are persisted")
		interval   = flag.Duration("interval", 5*time.Minute, "how often to flush snapshots")
		fullEvery  = flag.Int("full-every", 12, "take a full reference snapshot every N flushes")
	)
	flag.Parse()

	config, err := buildKubeConfig(*kubeconfig)
	if err != nil {
		return fmt.Errorf("build kube config: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("build kube client: %w", err)
	}

	store, err := storage.NewLocal(*storageDir)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}

	buf := agent.NewBuffer()
	snap := agent.NewSnapshotter(buf, store, *interval, *fullEvery)
	inf := agent.NewInformers(client, buf, 0)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("ktm-agent: starting",
		"storageDir", *storageDir,
		"interval", interval.String(),
		"fullEvery", *fullEvery)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return inf.Start(gctx) })
	g.Go(func() error { return snap.Run(gctx) })

	runErr := g.Wait()

	// Best-effort final flush: capture whatever happened between the
	// last periodic flush and the SIGTERM, so we don't lose the closing
	// window. Use a fresh, short-timeout context — the parent is
	// already cancelled.
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer flushCancel()
	if _, ferr := snap.Flush(flushCtx); ferr != nil {
		slog.Error("ktm-agent: final flush failed", "err", ferr)
	} else {
		slog.Info("ktm-agent: final flush succeeded")
	}

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("agent stopped: %w", runErr)
	}
	slog.Info("ktm-agent: stopped cleanly")
	return nil
}

// buildKubeConfig resolves the Kubernetes client configuration in the
// standard kubectl/kubelet order:
//
//  1. The explicit --kubeconfig flag, if non-empty.
//  2. In-cluster configuration (only succeeds when running as a pod).
//  3. $KUBECONFIG environment variable.
//  4. $HOME/.kube/config.
//
// The first one to succeed wins.
func buildKubeConfig(explicit string) (*rest.Config, error) {
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
	return nil, fmt.Errorf("no Kubernetes config found (tried --kubeconfig, in-cluster, $KUBECONFIG, $HOME/.kube/config)")
}
