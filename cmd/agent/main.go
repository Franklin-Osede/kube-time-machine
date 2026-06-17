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
//	--health-addr   HTTP listen address for /healthz and /readyz (empty = disabled)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Franklin-Osede/kube-time-machine/internal/agent"
	"github.com/Franklin-Osede/kube-time-machine/internal/health"
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
		kubeconfig        = flag.String("kubeconfig", "", "path to kubeconfig (empty = try in-cluster, fall back to $KUBECONFIG, then $HOME/.kube/config)")
		storageDir        = flag.String("storage-dir", "/var/lib/ktm", "directory where snapshots are persisted")
		interval          = flag.Duration("interval", 5*time.Minute, "how often to flush snapshots")
		fullEvery         = flag.Int("full-every", 12, "take a full reference snapshot every N flushes")
		healthAddr        = flag.String("health-addr", ":8080", "HTTP listen address for /healthz and /readyz (empty = disabled)")
		excludeNamespaces = flag.String("exclude-namespaces", "kube-system,kube-public,kube-node-lease", "comma-separated list of namespaces to exclude from watching")
		watchResources    = flag.String("watch-resources",
			"statefulsets.apps/v1,services/v1,ingresses.networking.k8s.io/v1,horizontalpodautoscalers.autoscaling/v2",
			"comma-separated list of resource[.group[/version]] to watch via dynamic informers (complements typed Deployment/ConfigMap watchers)")
		burstThreshold = flag.Int("burst-threshold", 50,
			"flush early when this many changes accumulate before the next periodic tick; 0 disables burst flushing")
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
	dynClient, err := kubeclient.NewDynamicClient(*kubeconfig)
	if err != nil {
		return fmt.Errorf("build dynamic kube client: %w", err)
	}

	store, err := storage.NewLocal(*storageDir)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}

	excludeNS := splitTrimmed(*excludeNamespaces)

	gvrs, err := agent.ParseGVRList(*watchResources)
	if err != nil {
		return fmt.Errorf("parse --watch-resources: %w", err)
	}

	buf := agent.NewBuffer()
	snap := agent.NewSnapshotter(buf, store, *interval, *fullEvery).
		WithBurstFlush(*burstThreshold, 10*time.Second)
	inf := agent.NewInformers(client, buf, 0, excludeNS)
	dynInf := agent.NewDynamicInformers(dynClient, buf, 0, gvrs, excludeNS)

	// Gate the Snapshotter on BOTH typed and dynamic informers syncing.
	// This ensures the first (always-full) snapshot is complete across all
	// watched resource kinds.
	allReady := agent.CombinedReady(inf.Ready(), dynInf.Ready())

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("ktm-agent: starting",
		"storageDir", *storageDir,
		"interval", interval.String(),
		"fullEvery", *fullEvery,
		"healthAddr", *healthAddr,
		"excludeNamespaces", excludeNS,
		"dynamicResources", *watchResources)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return inf.Start(gctx) })
	g.Go(func() error { return dynInf.Start(gctx) })
	healthSrv := health.New(*healthAddr, func() bool { return health.Ready(allReady) }).
		WithMetrics(func() string { return agentMetrics(buf, snap) })
	g.Go(func() error { return healthSrv.Run(gctx) })
	// snap.Run waits on allReady before its first flush so the first
	// (always-full) snapshot reflects a fully-synced cluster, not the
	// partial buffer that exists during the informers' initial list/watch.
	g.Go(func() error { return snap.Run(gctx, allReady) })

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
	case <-allReady:
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

// agentMetrics returns a Prometheus text-format metrics page sampling the
// current buffer and snapshotter state. Called on every /metrics request;
// no caching needed at this cardinality.
func agentMetrics(buf *agent.Buffer, snap *agent.Snapshotter) string {
	var b strings.Builder
	entries := buf.Len()
	pending := buf.PendingChanges()
	flushFull, flushDelta := snap.FlushCounts()

	b.WriteString("# HELP ktm_buffer_entries Current number of entries in the in-memory buffer.\n")
	b.WriteString("# TYPE ktm_buffer_entries gauge\n")
	fmt.Fprintf(&b, "ktm_buffer_entries %d\n\n", entries)

	b.WriteString("# HELP ktm_buffer_pending_changes Change events accumulated since the last flush.\n")
	b.WriteString("# TYPE ktm_buffer_pending_changes gauge\n")
	fmt.Fprintf(&b, "ktm_buffer_pending_changes %d\n\n", pending)

	b.WriteString("# HELP ktm_flushes_total Total number of successful snapshot flushes since startup.\n")
	b.WriteString("# TYPE ktm_flushes_total counter\n")
	fmt.Fprintf(&b, "ktm_flushes_total{kind=\"full\"} %d\n", flushFull)
	fmt.Fprintf(&b, "ktm_flushes_total{kind=\"delta\"} %d\n", flushDelta)

	return b.String()
}

// splitTrimmed splits a comma-separated string and trims each element.
// Empty elements (from trailing commas or empty input) are omitted.
func splitTrimmed(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
