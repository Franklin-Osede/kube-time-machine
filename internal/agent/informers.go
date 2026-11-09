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

// Informers wires shared informers for Deployments and ConfigMaps into a
// Buffer. Other kinds are recorded by DynamicInformers.
type Informers struct {
	factory   informers.SharedInformerFactory
	deployInf cache.SharedIndexInformer
	cmInf     cache.SharedIndexInformer
	buf       *Buffer
	excludeNS map[string]struct{}
	ready     chan struct{}
}

// NewInformers constructs Informers wired to buf. A resync of 0 disables
// periodic resyncs: the watch already keeps the cache coherent. Events from
// excludeNamespaces never reach the buffer.
func NewInformers(client kubernetes.Interface, buf *Buffer, resync time.Duration, excludeNamespaces []string) *Informers {
	factory := informers.NewSharedInformerFactory(client, resync)
	excludeNS := make(map[string]struct{}, len(excludeNamespaces))
	for _, ns := range excludeNamespaces {
		if ns != "" {
			excludeNS[ns] = struct{}{}
		}
	}
	in := &Informers{
		factory:   factory,
		deployInf: factory.Apps().V1().Deployments().Informer(),
		cmInf:     factory.Core().V1().ConfigMaps().Informer(),
		buf:       buf,
		excludeNS: excludeNS,
		ready:     make(chan struct{}),
	}
	// AddEventHandler fails only on an already-started informer; these are
	// fresh, so an error here is a programming bug.
	if _, err := in.deployInf.AddEventHandler(in.deploymentHandler()); err != nil {
		panic(fmt.Errorf("agent: register Deployment handler: %w", err))
	}
	if _, err := in.cmInf.AddEventHandler(in.configMapHandler()); err != nil {
		panic(fmt.Errorf("agent: register ConfigMap handler: %w", err))
	}
	return in
}

// Start runs the informers and blocks until ctx is cancelled, returning
// ctx.Err(). There is no sync timeout: if the initial list never completes,
// Ready never closes and /readyz keeps failing.
func (i *Informers) Start(ctx context.Context) error {
	i.factory.Start(ctx.Done())
	// WaitForCacheSync returns false only when ctx is done.
	if !cache.WaitForCacheSync(ctx.Done(), i.deployInf.HasSynced, i.cmInf.HasSynced) {
		return ctx.Err()
	}
	// Closed only after a complete sync, so nothing waiting on Ready can
	// persist a partial view of the cluster.
	close(i.ready)
	slog.Info("agent: informer caches synced")
	<-ctx.Done()
	return ctx.Err()
}

// Ready returns a channel closed once the initial cache sync completes. It
// never closes if ctx is cancelled first.
func (i *Informers) Ready() <-chan struct{} {
	return i.ready
}

// isExcluded reports whether ns is in the exclude list. Excluded namespaces
// are still listed and cached; only their events are dropped.
func (i *Informers) isExcluded(ns string) bool {
	_, ok := i.excludeNS[ns]
	return ok
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
			if i.isExcluded(d.Namespace) {
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
	if i.isExcluded(d.Namespace) {
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
			if i.isExcluded(cm.Namespace) {
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
	if i.isExcluded(cm.Namespace) {
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

// unwrapDelete handles DeletedFinalStateUnknown: when the watch missed the
// final state, client-go delivers the last known object wrapped in a
// tombstone. Its key is all a delete needs.
func unwrapDelete(obj any) any {
	if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		return t.Obj
	}
	return obj
}
