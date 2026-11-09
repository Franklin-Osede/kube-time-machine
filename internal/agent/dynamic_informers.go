package agent

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
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// DynamicInformers watches a configurable set of GroupVersionResources,
// including CRDs, and writes sanitised state into the Buffer shared with the
// typed Informers. ParseGVRList and CheckDynamicKinds refuse any GVR whose
// Kind another source already writes, so no two sources write the same Key.
// It is the Phase 2 extension point: blame and rollback support only the
// typed kinds.
type DynamicInformers struct {
	factory     dynamicinformer.DynamicSharedInformerFactory
	gvrs        []schema.GroupVersionResource
	buf         *Buffer
	excludeNS   map[string]struct{}
	ready       chan struct{}
	syncTimeout time.Duration
}

// NewDynamicInformers constructs a DynamicInformers watching gvrs.
// excludeNamespaces behaves as on NewInformers.
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

// Ready returns a channel closed once every object in the initial lists is in
// the buffer.
func (d *DynamicInformers) Ready() <-chan struct{} {
	return d.ready
}

// WithSyncTimeout bounds the wait for the initial cache sync; Start returns
// an error when it expires. Zero, the default, waits until ctx is cancelled.
// Without it, a GVR missing from the cluster blocks the sync forever.
func (d *DynamicInformers) WithSyncTimeout(timeout time.Duration) *DynamicInformers {
	d.syncTimeout = timeout
	return d
}

// Start runs the informers and blocks until ctx is cancelled, returning
// ctx.Err(), or returns an error if the initial sync times out.
func (d *DynamicInformers) Start(ctx context.Context) error {
	hasSynced := make([]cache.InformerSynced, 0, len(d.gvrs))

	for _, gvr := range d.gvrs {
		inf := d.factory.ForResource(gvr).Informer()
		gvrCopy := gvr

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
		reg, err := inf.AddEventHandler(handler)
		if err != nil {
			return fmt.Errorf("agent: dynamic: register handler for %s: %w", gvr.Resource, err)
		}
		// The handler's sync, not the informer's: see Informers.synced.
		hasSynced = append(hasSynced, reg.HasSynced)
	}

	d.factory.Start(ctx.Done())

	syncCtx := ctx
	var syncCancel context.CancelFunc
	if d.syncTimeout > 0 {
		syncCtx, syncCancel = context.WithTimeout(ctx, d.syncTimeout)
		defer syncCancel()
	}

	if !cache.WaitForCacheSync(syncCtx.Done(), hasSynced...) {
		if err := ctx.Err(); err != nil {
			return err // cancelled, not timed out
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

// toUnstructured casts obj to the type dynamic informers deliver.
func toUnstructured(obj any) (*unstructured.Unstructured, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	return u, ok
}

// marshalUnstructured sanitises a dynamic object exactly as the typed path
// does, including .status, and serialises it. It works on a DeepCopy, so the
// informer's shared object is never mutated.
func marshalUnstructured(u *unstructured.Unstructured) (delta.Key, delta.State, error) {
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

// sanitiseUnstructuredMeta applies sanitiseMeta's rules to an unstructured
// object.
func sanitiseUnstructuredMeta(u *unstructured.Unstructured) {
	managers := extractManagers(u.GetManagedFields())

	u.SetResourceVersion("")
	u.SetManagedFields(nil)
	u.SetGeneration(0)

	anns := u.GetAnnotations()
	if anns == nil {
		anns = make(map[string]string)
	}
	for k := range anns {
		if isNoiseAnnotationKey(k) {
			delete(anns, k)
		}
	}
	// After stripping, so the synthetic annotation can never be removed.
	if len(managers) > 0 {
		anns[types.ManagersAnnotation] = strings.Join(managers, ",")
	}
	if len(anns) == 0 {
		u.SetAnnotations(nil)
	} else {
		u.SetAnnotations(anns)
	}
}

// ParseGVR parses a "resource[.group]/version" string, such as
// "statefulsets.apps/v1" or "services/v1", into a GroupVersionResource.
// Version is required because the dynamic client does not perform discovery.
func ParseGVR(s string) (schema.GroupVersionResource, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("agent: empty GVR string")
	}

	if strings.Count(s, "/") != 1 {
		return schema.GroupVersionResource{}, fmt.Errorf("agent: GVR %q must be resource[.group]/version", s)
	}
	resourceGroup, version, _ := strings.Cut(s, "/")
	if version == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("agent: GVR %q has an empty version", s)
	}

	parts := strings.SplitN(resourceGroup, ".", 2)
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

// typedGroupResources are recorded by the typed Informers. Watching them
// dynamically too would write the same Key from two serialisations that
// differ in field order and defaults, so every event would flip the stored
// state and show up as a change.
var typedGroupResources = map[schema.GroupResource]bool{
	{Group: "apps", Resource: "deployments"}: true,
	{Group: "", Resource: "configmaps"}:      true,
}

// refusedGroupResources are never recorded: snapshots are stored unencrypted
// on the PVC and read by anyone with access to it.
var refusedGroupResources = map[schema.GroupResource]string{
	{Group: "", Resource: "secrets"}: "Secrets are not recorded: snapshots are stored unencrypted on the volume",
}

// ParseGVRList parses a comma-separated list of GVR strings. Whitespace
// around each entry is trimmed and empty entries are ignored. Deployments
// and ConfigMaps are refused, because the typed Informers already record
// them, and so are Secrets.
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
		if typedGroupResources[gvr.GroupResource()] {
			return nil, fmt.Errorf("agent: %q is already recorded by the built-in watches; remove it from --watch-resources", part)
		}
		if why, ok := refusedGroupResources[gvr.GroupResource()]; ok {
			return nil, fmt.Errorf("agent: %q: %s; remove it from --watch-resources", part, why)
		}
		out = append(out, gvr)
	}
	return out, nil
}

// CombinedReady returns a channel that is closed once both a and b are
// closed.
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

// typedKinds are the Kinds the typed Informers write.
var typedKinds = map[string]string{"Deployment": "apps/v1", "ConfigMap": "v1"}

// CheckDynamicKinds asks the API server for the Kind of each GVR and refuses
// a configuration in which two sources would write the same Kind. A Key
// carries no API group, so two kinds with the same name, such as cert-manager
// and Knative Certificates, would overwrite each other's history.
//
// It checks only the configuration it is given. History recorded under a
// different --watch-resources list can still share a Kind with this one;
// docs/install.md tells operators to use a new store when they change it.
// Any discovery error is returned, so the agent never starts half-checked.
func CheckDynamicKinds(d discovery.DiscoveryInterface, gvrs []schema.GroupVersionResource) error {
	owner := make(map[string]string, len(gvrs)+len(typedKinds))
	for kind, src := range typedKinds {
		owner[kind] = src + " (built-in watch)"
	}
	for _, gvr := range gvrs {
		gv := gvr.GroupVersion().String()
		list, err := d.ServerResourcesForGroupVersion(gv)
		if err != nil {
			return fmt.Errorf("agent: discover %s: %w", gvr.String(), err)
		}
		kind := ""
		for _, r := range list.APIResources {
			if r.Name == gvr.Resource {
				kind = r.Kind
				break
			}
		}
		if kind == "" {
			return fmt.Errorf("agent: the API server does not serve %q in %s", gvr.Resource, gv)
		}
		src := gvr.Resource + "." + gv
		if prev, ok := owner[kind]; ok {
			return fmt.Errorf("agent: %s and %s both have Kind %q; snapshots key resources by Kind without the API group, so they would overwrite each other. Watch only one of them", prev, src, kind)
		}
		owner[kind] = src
	}
	return nil
}
