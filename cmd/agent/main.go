// Command ktm-agent watches Deployments and ConfigMaps through client-go
// informers and persists periodic snapshots (full + deltas) to a local
// directory, in-cluster on a PersistentVolume or locally against a
// kubeconfig. Flags: ktm-agent -help.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Franklin-Osede/kube-time-machine/internal/agent"
	"github.com/Franklin-Osede/kube-time-machine/internal/health"
	"github.com/Franklin-Osede/kube-time-machine/internal/kubeclient"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// version is set at release time with -ldflags="-X main.version=...".
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
		healthAddr        = flag.String("health-addr", "127.0.0.1:8080", "HTTP listen address for /healthz, /readyz and /metrics; the Helm chart sets :8080 so kubelet can reach it (empty = disabled)")
		excludeNamespaces = flag.String("exclude-namespaces", "kube-system,kube-public,kube-node-lease", "comma-separated list of namespaces to exclude from watching")
		watchResources    = flag.String("watch-resources",
			"",
			"comma-separated list of resource[.group]/version to watch via dynamic informers (complements typed Deployment/ConfigMap watchers)")
		burstThreshold = flag.Int("burst-threshold", 50,
			"flush early when this many changes accumulate before the next periodic tick; 0 disables burst flushing")
		retainDays  = flag.Int("retain-days", 30, "delete snapshots older than this many days, checked before every flush; 0 keeps all snapshots forever")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if err := validateConfig(*interval, *fullEvery, *burstThreshold, *retainDays); err != nil {
		return err
	}
	memLimit := setMemoryLimit(os.Getenv)

	client, err := kubeclient.NewClient(*kubeconfig)
	if err != nil {
		return fmt.Errorf("build kube client: %w", err)
	}
	dynClient, err := kubeclient.NewDynamicClient(*kubeconfig)
	if err != nil {
		return fmt.Errorf("build dynamic kube client: %w", err)
	}

	// NewLocal takes the writer lock before it repairs anything.
	store, err := storage.NewLocal(*storageDir)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			slog.Error("ktm-agent: close storage", "err", err)
		}
	}()

	excludeNS := splitTrimmed(*excludeNamespaces)

	gvrs, err := agent.ParseGVRList(*watchResources)
	if err != nil {
		return fmt.Errorf("parse --watch-resources: %w", err)
	}
	if len(gvrs) > 0 {
		if err := agent.CheckDynamicKinds(client.Discovery(), gvrs); err != nil {
			return fmt.Errorf("check --watch-resources: %w", err)
		}
	}

	buf := agent.NewBuffer()
	snap := agent.NewSnapshotter(buf, store, *interval, *fullEvery).
		WithBurstFlush(*burstThreshold, 10*time.Second).
		WithRetention(*retainDays)
	inf := agent.NewInformers(client, buf, 0, excludeNS)
	dynInf := agent.NewDynamicInformers(dynClient, buf, 0, gvrs, excludeNS)
	if len(gvrs) > 0 {
		dynInf = dynInf.WithSyncTimeout(2 * time.Minute)
	}

	// Nothing is persisted before both informer sets have synced: a partial
	// cache must never become a full snapshot.
	allReady := agent.CombinedReady(inf.Ready(), dynInf.Ready())

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("ktm-agent: starting",
		"storageDir", *storageDir,
		"interval", interval.String(),
		"fullEvery", *fullEvery,
		"retainDays", *retainDays,
		"healthAddr", *healthAddr,
		"excludeNamespaces", excludeNS,
		"dynamicResources", *watchResources,
		"goMemoryLimitBytes", memLimit)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return inf.Start(gctx) })
	g.Go(func() error { return dynInf.Start(gctx) })
	healthSrv := health.New(*healthAddr, func() bool {
		return health.Ready(allReady) && snap.FlushHealthy(3)
	}).
		WithMetrics(func() string { return agentMetrics(buf, snap) })
	g.Go(func() error { return healthSrv.Run(gctx) })
	g.Go(func() error { return snap.Run(gctx, allReady) })

	runErr := g.Wait()
	finalFlush(allReady, snap)

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("agent stopped: %w", runErr)
	}
	slog.Info("ktm-agent: stopped cleanly")
	return nil
}

// flusher is the part of *agent.Snapshotter that finalFlush needs.
type flusher interface {
	Flush(ctx context.Context) (types.SnapshotMeta, error)
}

// finalFlush writes one last snapshot on shutdown, and only if the informer
// caches synced: an empty or partial cache must never become a full
// snapshot. ctx is already cancelled by then, so the flush gets its own
// budget. It reports whether a flush was attempted.
func finalFlush(ready <-chan struct{}, snap flusher) bool {
	select {
	case <-ready:
	default:
		slog.Info("ktm-agent: skipping final flush; informer caches never synced")
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := snap.Flush(ctx); err != nil {
		slog.Error("ktm-agent: final flush failed", "err", err)
	} else {
		slog.Info("ktm-agent: final flush succeeded")
	}
	return true
}

func validateConfig(interval time.Duration, fullEvery, burstThreshold, retainDays int) error {
	// Snapshot IDs have millisecond resolution and a collision fails the
	// flush; a one-second floor keeps periodic flushes well apart.
	if interval < time.Second {
		return fmt.Errorf("--interval must be at least 1s")
	}
	if fullEvery < 1 {
		return fmt.Errorf("--full-every must be at least 1")
	}
	if burstThreshold < 0 {
		return fmt.Errorf("--burst-threshold must be non-negative")
	}
	if retainDays < 0 {
		return fmt.Errorf("--retain-days must be non-negative")
	}
	return nil
}

// agentMetrics renders the /metrics page in Prometheus text format,
// sampled fresh on every request.
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

// splitTrimmed splits a comma-separated string, trims each element and
// drops empty ones.
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

// memoryLimitEnv carries the container's memory limit in bytes. The chart
// sets it from the downward API.
const memoryLimitEnv = "KTM_MEMORY_LIMIT"

// setMemoryLimit gives the Go GC a soft limit at 90% of the container's
// memory limit and returns it, or returns 0 and changes nothing when
// GOMEMLIMIT is set or the limit is unknown. It makes the GC work harder as
// runtime memory nears the limit; it does not cap container memory, and the
// runtime exceeds it rather than spend too long in GC.
func setMemoryLimit(getenv func(string) string) int64 {
	if getenv("GOMEMLIMIT") != "" {
		return 0
	}
	n, err := strconv.ParseInt(getenv(memoryLimitEnv), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	limit := n / 10 * 9
	debug.SetMemoryLimit(limit)
	return limit
}
