package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// Informers wires Kubernetes shared informers for Deployments and
// ConfigMaps into a Buffer. Every Add / Update event is marshalled and
// upserted; every Delete event removes the corresponding Key.
//
// Scope-lock: only Deployments and ConfigMaps are watched. Adding more
// kinds in Phase 2 means adding new informers here and a marshal
// function in marshal.go — nothing else changes.
type Informers struct {
	factory   informers.SharedInformerFactory
	deployInf cache.SharedIndexInformer
	cmInf     cache.SharedIndexInformer
	buf       *Buffer
	ready     chan struct{} // closed once the initial cache sync completes
}

// NewInformers constructs Informers wired to buf.
//
// resync is the cadence for periodic informer cache resyncs. Pass 0 to
// disable resync — the K8s API guarantees the informer cache stays
// coherent via watch, so resync is paranoia, not correctness. Enable
// only if real-world drift appears.
func NewInformers(client kubernetes.Interface, buf *Buffer, resync time.Duration) *Informers {
	factory := informers.NewSharedInformerFactory(client, resync)
	in := &Informers{
		factory:   factory,
		deployInf: factory.Apps().V1().Deployments().Informer(),
		cmInf:     factory.Core().V1().ConfigMaps().Informer(),
		buf:       buf,
		ready:     make(chan struct{}),
	}
	// AddEventHandler can fail (e.g. if the informer was already
	// started). At construction time the informers are fresh, so the
	// only realistic failure is programmer error — surface it via panic
	// rather than swallowing.
	if _, err := in.deployInf.AddEventHandler(in.deploymentHandler()); err != nil {
		panic(fmt.Errorf("agent: register Deployment handler: %w", err))
	}
	if _, err := in.cmInf.AddEventHandler(in.configMapHandler()); err != nil {
		panic(fmt.Errorf("agent: register ConfigMap handler: %w", err))
	}
	return in
}

// Start launches the informer goroutines and blocks until ctx is
// cancelled. It returns an error only if the initial cache sync fails;
// after sync, the underlying informers run until ctx ends.
//
// Callers MUST call Start before relying on Buffer contents. Without
// sync, the buffer reflects only events received so far, which may be
// a partial view.
func (i *Informers) Start(ctx context.Context) error {
	i.factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), i.deployInf.HasSynced, i.cmInf.HasSynced) {
		// WaitForCacheSync returns false in two distinct cases: the
		// context was cancelled, or sync genuinely failed. Disambiguate
		// so callers can distinguish a shutdown from a real error.
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("agent: informer cache sync failed")
	}
	// Signal readiness so the Snapshotter can take its first flush against
	// a complete buffer. Closed exactly once, only on a successful sync —
	// callers waiting on Ready() will keep waiting if sync fails or the
	// context is cancelled first, which is the correct behaviour (no
	// partial snapshot is ever taken).
	close(i.ready)
	slog.Info("agent: informer caches synced")
	<-ctx.Done()
	return ctx.Err()
}

// Ready returns a channel that is closed once the initial informer cache
// sync has completed successfully. It is the synchronisation point the
// Snapshotter waits on before its first flush; see the contract note on
// Start. The channel is never closed if Start exits due to a sync failure
// or a cancelled context.
func (i *Informers) Ready() <-chan struct{} {
	return i.ready
}

func (i *Informers) deploymentHandler() cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			i.handleDeploymentUpsert(obj, "add")
		},
		UpdateFunc: func(_, newObj any) {
			i.handleDeploymentUpsert(newObj, "update")
		},
		DeleteFunc: func(obj any) {
			d, ok := unwrapDelete(obj).(*appsv1.Deployment)
			if !ok {
				slog.Warn("agent: Deployment delete handler got unexpected type",
					"type", fmt.Sprintf("%T", obj))
				return
			}
			i.buf.Delete(KeyForDeployment(d))
		},
	}
}

func (i *Informers) handleDeploymentUpsert(obj any, op string) {
	d, ok := obj.(*appsv1.Deployment)
	if !ok {
		slog.Warn("agent: Deployment "+op+" handler got unexpected type",
			"type", fmt.Sprintf("%T", obj))
		return
	}
	key, state, err := MarshalDeployment(d)
	if err != nil {
		slog.Error("agent: marshal Deployment failed",
			"op", op, "namespace", d.Namespace, "name", d.Name, "err", err)
		return
	}
	i.buf.Upsert(key, state)
}

func (i *Informers) configMapHandler() cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			i.handleConfigMapUpsert(obj, "add")
		},
		UpdateFunc: func(_, newObj any) {
			i.handleConfigMapUpsert(newObj, "update")
		},
		DeleteFunc: func(obj any) {
			cm, ok := unwrapDelete(obj).(*corev1.ConfigMap)
			if !ok {
				slog.Warn("agent: ConfigMap delete handler got unexpected type",
					"type", fmt.Sprintf("%T", obj))
				return
			}
			i.buf.Delete(KeyForConfigMap(cm))
		},
	}
}

func (i *Informers) handleConfigMapUpsert(obj any, op string) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		slog.Warn("agent: ConfigMap "+op+" handler got unexpected type",
			"type", fmt.Sprintf("%T", obj))
		return
	}
	key, state, err := MarshalConfigMap(cm)
	if err != nil {
		slog.Error("agent: marshal ConfigMap failed",
			"op", op, "namespace", cm.Namespace, "name", cm.Name, "err", err)
		return
	}
	i.buf.Upsert(key, state)
}

// unwrapDelete handles the DeletedFinalStateUnknown case: when the
// informer lost watch events and can't tell us the final state, it
// delivers a tombstone wrapping the last known object. We only need the
// Key to remove the entry from the buffer, so unwrapping is enough.
func unwrapDelete(obj any) any {
	if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		return t.Obj
	}
	return obj
}
