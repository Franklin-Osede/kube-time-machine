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
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"os/signal"

	"github.com/Franklin-Osede/kube-time-machine/internal/agent"
	"github.com/Franklin-Osede/kube-time-machine/internal/kubeclient"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
)

// version is stamped at release time via -ldflags="-X main.version=...".
// Mirrors the CLI so both binaries report a consistent version.
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("ktm-agent: fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		kubeconfig  = flag.String("kubeconfig", "", "path to kubeconfig (empty = try in-cluster, fall back to $KUBECONFIG, then $HOME/.kube/config)")
		storageDir  = flag.String("storage-dir", "/var/lib/ktm", "directory where snapshots are persisted")
		interval    = flag.Duration("interval", 5*time.Minute, "how often to flush snapshots")
		fullEvery   = flag.Int("full-every", 12, "take a full reference snapshot every N flushes")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	client, err := kubeclient.NewClient(*kubeconfig)
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
	// snap.Run waits on inf.Ready() before its first flush so the first
	// (always-full) snapshot reflects a fully-synced cluster, not the
	// partial buffer that exists during the informers' initial list/watch.
	g.Go(func() error { return snap.Run(gctx, inf.Ready()) })

	runErr := g.Wait()

	// Best-effort final flush: capture whatever happened between the last
	// periodic flush and the SIGTERM, so we don't lose the closing window.
	// Gated on inf.Ready() for the same reason Snapshotter.Run is — if the
	// agent is cancelled before the informer caches sync, the buffer holds
	// only a partial view, and flushing it would persist a misleading full
	// snapshot. A non-blocking receive distinguishes "synced" from "never
	// synced" without waiting. Use a fresh, short-timeout context — the
	// parent is already cancelled.
	select {
	case <-inf.Ready():
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer flushCancel()
		if _, ferr := snap.Flush(flushCtx); ferr != nil {
			slog.Error("ktm-agent: final flush failed", "err", ferr)
		} else {
			slog.Info("ktm-agent: final flush succeeded")
		}
	default:
		slog.Info("ktm-agent: skipping final flush; informer caches never synced")
	}

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("agent stopped: %w", runErr)
	}
	slog.Info("ktm-agent: stopped cleanly")
	return nil
}
