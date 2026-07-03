package agent

// DynamicInformers extends the typed Informers with support for any
// Kubernetes resource kind, including CRDs, without requiring generated
// typed structs. It is the Phase 2 extension point: typed informers cover
// Deployments and ConfigMaps with full type safety; DynamicInformers covers
// everything else (StatefulSets, Services, Ingresses, HPAs, etc.) generically.
//
// Both share the same Buffer — their Keys are disjoint by Kind, so there is
// no collision risk.
//
// Usage:
//
//	dynInf := agent.NewDynamicInformers(dynClient, buf, resync, gvrs, excludeNS)
//	go dynInf.Start(ctx)
//	<-dynInf.Ready()

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
)

// KTMManagedAnnotation is the synthetic annotation key injected by KTM
// to record which SSA managers last touched a resource.
// Defined here as a package-level constant so blame and diff can reference
// it without importing internal/agent.
const KTMManagedAnnotation = "ktm.io/managers"

// DefaultDynamicGVRs is the default set of resources watched by
// DynamicInformers. These complement the Deployments and ConfigMaps covered
// by typed Informers and represent the next tier of operational importance.
var DefaultDynamicGVRs = []schema.GroupVersionResource{
	{Group: "apps", Version: "v1", Resource: "statefulsets"},
	{Group: "", Version: "v1", Resource: "services"},
	{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
	{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"},
}

// DynamicInformers watches a configurable set of GroupVersionResources and
// writes sanitised snapshots into a shared Buffer.
type DynamicInformers struct {
	factory     dynamicinformer.DynamicSharedInformerFactory
	gvrs        []schema.GroupVersionResource
	buf         *Buffer
	excludeNS   map[string]struct{}
	ready       chan struct{} // closed once all informer caches are synced
	syncTimeout time.Duration
}

// NewDynamicInformers constructs a DynamicInformers. gvrs is the list of
// resources to watch; pass DefaultDynamicGVRs for the standard set.
// excludeNamespaces mirrors the same parameter on typed Informers.
func NewDynamicInformers(
	client dynamic.Interface,
	buf *Buffer,
	resync time.Duration,
	gvrs []schema.GroupVersionResource,
	excludeNamespaces []string,
) *DynamicInformers {
	factory := dynamicinformer.NewDynamicSharedInformerFactory(client, resync)
	excludeNS := make(map[string]struct{}, len(excludeNamespaces))
	for _, ns := range excludeNamespaces {
		if ns != "" {
			excludeNS[ns] = struct{}{}
		}
	}
	return &DynamicInformers{
		factory:   factory,
		gvrs:      gvrs,
		buf:       buf,
		excludeNS: excludeNS,
		ready:     make(chan struct{}),
	}
}

// Ready returns a channel closed once all informer caches have synced.
// Mirrors the contract on typed Informers so callers can gate snapshots on
// both.
func (d *DynamicInformers) Ready() <-chan struct{} {
	return d.ready
}

// WithSyncTimeout sets the maximum duration to wait for all informer caches
// to sync before Start() returns an error. When zero (the default), Start()
// waits until ctx is cancelled. Set this when watchResources may name GVRs
// that don't exist in the cluster — without a timeout, WaitForCacheSync
// blocks the agent indefinitely while the pod stays Running-but-not-Ready.
func (d *DynamicInformers) WithSyncTimeout(timeout time.Duration) *DynamicInformers {
	d.syncTimeout = timeout
	return d
}

// Start wires event handlers for every GVR, starts the factory, waits for
// all caches to sync, and then blocks until ctx is cancelled. Returns
// ctx.Err() on clean shutdown or an error if initial sync fails.
func (d *DynamicInformers) Start(ctx context.Context) error {
	hasSynced := make([]cache.InformerSynced, 0, len(d.gvrs))

	for _, gvr := range d.gvrs {
		inf := d.factory.ForResource(gvr).Informer()
		gvrCopy := gvr // capture for closure

		handler := cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				d.handleUpsert(obj, gvrCopy, "add")
			},
			UpdateFunc: func(_, newObj any) {
				d.handleUpsert(newObj, gvrCopy, "update")
			},
			DeleteFunc: func(obj any) {
				d.handleDelete(obj, gvrCopy)
			},
		}
		if _, err := inf.AddEventHandler(handler); err != nil {
			return fmt.Errorf("agent: dynamic: register handler for %s: %w", gvr.Resource, err)
		}
		hasSynced = append(hasSynced, inf.HasSynced)
	}

	d.factory.Start(ctx.Done())

	// When a syncTimeout is configured, use a derived context so that a
	// non-existent or RBAC-denied GVR surfaces a loud error quickly instead
	// of blocking WaitForCacheSync until the parent context (SIGTERM) fires.
	syncCtx := ctx
	var syncCancel context.CancelFunc
	if d.syncTimeout > 0 {
		syncCtx, syncCancel = context.WithTimeout(ctx, d.syncTimeout)
		defer syncCancel()
	}

	if !cache.WaitForCacheSync(syncCtx.Done(), hasSynced...) {
		if err := ctx.Err(); err != nil {
			return err // parent cancelled (SIGTERM) — not a timeout
		}
		return fmt.Errorf("agent: dynamic: informer cache sync timed out after %s (GVR not found or RBAC denied?)", d.syncTimeout)
	}
	close(d.ready)
	slog.Info("agent: dynamic informer caches synced", "resources", d.resourceNames())
	<-ctx.Done()
	return ctx.Err()
}

func (d *DynamicInformers) handleUpsert(obj any, gvr schema.GroupVersionResource, op string) {
	u, ok := toUnstructured(obj)
	if !ok {
		slog.Warn("agent: dynamic upsert got unexpected type",
			"resource", gvr.Resource, "op", op, "type", fmt.Sprintf("%T", obj))
		return
	}
	if d.isExcluded(u.GetNamespace()) {
		return
	}
	key, state, err := marshalUnstructured(u)
	if err != nil {
		slog.Error("agent: dynamic marshal failed",
			"resource", gvr.Resource, "op", op,
			"namespace", u.GetNamespace(), "name", u.GetName(), "err", err)
		return
	}
	d.buf.Upsert(key, state)
}

func (d *DynamicInformers) handleDelete(obj any, gvr schema.GroupVersionResource) {
	raw := obj
	if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		raw = t.Obj
	}
	u, ok := toUnstructured(raw)
	if !ok {
		slog.Warn("agent: dynamic delete got unexpected type",
			"resource", gvr.Resource, "type", fmt.Sprintf("%T", obj))
		return
	}
	if d.isExcluded(u.GetNamespace()) {
		return
	}
	d.buf.Delete(delta.Key{
		Kind:      u.GetKind(),
		Namespace: u.GetNamespace(),
		Name:      u.GetName(),
	})
}

func (d *DynamicInformers) isExcluded(ns string) bool {
	_, ok := d.excludeNS[ns]
	return ok
}

func (d *DynamicInformers) resourceNames() []string {
	names := make([]string, len(d.gvrs))
	for i, g := range d.gvrs {
		names[i] = g.Resource
	}
	return names
}

// toUnstructured casts obj to *unstructured.Unstructured, which is what
// dynamic informers deliver.
func toUnstructured(obj any) (*unstructured.Unstructured, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	return u, ok
}

// marshalUnstructured sanitises and serialises a dynamic Kubernetes object
// into the (Key, State) pair the Buffer expects. The sanitisation mirrors
// sanitiseMeta on the typed path: strips resourceVersion, managedFields,
// generation, status, and known noise annotations.
func marshalUnstructured(u *unstructured.Unstructured) (delta.Key, delta.State, error) {
	// DeepCopy so we never mutate the informer's shared object.
	clean := u.DeepCopy()
	sanitiseUnstructuredMeta(clean)
	delete(clean.Object, "status")

	b, err := json.Marshal(clean.Object)
	if err != nil {
		return delta.Key{}, nil, err
	}
	key := delta.Key{
		Kind:      clean.GetKind(),
		Namespace: clean.GetNamespace(),
		Name:      clean.GetName(),
	}
	return key, delta.State(b), nil
}

// sanitiseUnstructuredMeta removes noise fields from the metadata section of
// an unstructured object, applying the same rules as sanitiseMeta on the
// typed path. Manager names are extracted from managedFields before it is
// cleared and preserved as the synthetic annotation "ktm.io/managers".
func sanitiseUnstructuredMeta(u *unstructured.Unstructured) {
	// Extract manager names before clearing managedFields.
	// GetManagedFields() is implemented by Unstructured and returns
	// []metav1.ManagedFieldsEntry, so we can reuse the typed-path helper.
	managers := extractManagers(u.GetManagedFields())

	u.SetResourceVersion("")
	u.SetManagedFields(nil)
	u.SetGeneration(0)

	// Strip noise annotations inline on the unstructured annotations map.
	anns := u.GetAnnotations()
	if anns == nil {
		anns = make(map[string]string)
	}
	for k := range anns {
		if isNoiseAnnotationKey(k) {
			delete(anns, k)
		}
	}
	// Inject manager names AFTER stripping so the synthetic annotation
	// is never accidentally removed by isNoiseAnnotationKey.
	if len(managers) > 0 {
		anns[KTMManagedAnnotation] = strings.Join(managers, ",")
	}
	if len(anns) == 0 {
		u.SetAnnotations(nil)
	} else {
		u.SetAnnotations(anns)
	}
}

// isNoiseAnnotationKey returns true if the annotation key matches a known
// noise key or a noise prefix. Mirrors the logic in marshal.go so both
// the typed and dynamic paths apply identical filtering.
func isNoiseAnnotationKey(key string) bool {
	if _, exact := noiseAnnotationKeys[key]; exact {
		return true
	}
	for _, prefix := range noiseAnnotationPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// ParseGVR parses a "resource[.group[/version]]" string into a
// GroupVersionResource. The format is:
//
//	"statefulsets"                          → {Group:"", Version:"", Resource:"statefulsets"}
//	"statefulsets.apps"                     → {Group:"apps", Version:"", Resource:"statefulsets"}
//	"statefulsets.apps/v1"                  → {Group:"apps", Version:"v1", Resource:"statefulsets"}
//	"ingresses.networking.k8s.io/v1"        → {Group:"networking.k8s.io", Version:"v1", Resource:"ingresses"}
//
// Version is optional; when omitted, the client-go dynamic factory uses the
// preferred version reported by the API server's discovery endpoint.
// Returns an error if the string is empty or malformed.
func ParseGVR(s string) (schema.GroupVersionResource, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("agent: empty GVR string")
	}

	var version string
	if idx := strings.Index(s, "/"); idx >= 0 {
		version = s[idx+1:]
		s = s[:idx]
	}

	// s is now "resource[.group...]"
	parts := strings.SplitN(s, ".", 2)
	resource := parts[0]
	group := ""
	if len(parts) == 2 {
		group = parts[1]
	}
	if resource == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("agent: malformed GVR %q: resource part is empty", s)
	}
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}, nil
}

// ParseGVRList parses a comma-separated list of GVR strings. Whitespace
// around each entry is trimmed and empty entries are ignored.
func ParseGVRList(s string) ([]schema.GroupVersionResource, error) {
	var out []schema.GroupVersionResource
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		gvr, err := ParseGVR(part)
		if err != nil {
			return nil, err
		}
		out = append(out, gvr)
	}
	return out, nil
}

// CombinedReady returns a channel that is closed once BOTH a and b are
// closed. Used in main.go to gate the Snapshotter on both typed and dynamic
// informers being ready.
func CombinedReady(a, b <-chan struct{}) <-chan struct{} {
	out := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { <-a; wg.Done() }()
		go func() { <-b; wg.Done() }()
		wg.Wait()
		close(out)
	}()
	return out
}
