package k8s

import (
	"context"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

// gatedFakeDyn builds a dynamic.Interface that allows every list call, but
// runs gate() before answering. Lets a test hold probe goroutines mid-flight
// to model an old cluster's probe still running while a context switch
// invalidates the permission cache.
func gatedFakeDyn(t *testing.T, gate func()) dynamic.Interface {
	t.Helper()

	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{}
	perms := &ResourcePermissions{}
	for _, p := range resourceProbeTargets(perms) {
		gvrToListKind[p.gvr] = p.gvr.Resource + "List"
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
	client.PrependReactor("list", "*", func(clienttesting.Action) (bool, runtime.Object, error) {
		gate()
		return false, nil, nil // fall through to the default reactor (empty list => allowed)
	})
	return client
}

// TestStaleProbeResultDroppedAfterInvalidation models the context-switch
// race: a probe pass starts against the old cluster, the switch invalidates
// the permission cache, and only then does the old probe finish. Its publish
// must be dropped — otherwise the previous cluster's scopes sit in the cache
// with a fresh TTL and informer wiring reads them for the new cluster. A
// probe started after the invalidation must still publish normally.
func TestStaleProbeResultDroppedAfterInvalidation(t *testing.T) {
	// CheckResourcePermissions bails when the typed client is nil; point it
	// at a non-existent server — only buildScopeCandidates touches it, and
	// that fails fast.
	dummyClient, err := kubernetes.NewForConfig(&rest.Config{Host: "http://localhost:1"})
	if err != nil {
		t.Fatalf("creating dummy client: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	oldClusterDyn := gatedFakeDyn(t, func() {
		startOnce.Do(func() { close(started) })
		<-release
	})

	clientMu.Lock()
	k8sClient = dummyClient
	dynamicClient = oldClusterDyn
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		k8sClient = nil
		dynamicClient = nil
		clientMu.Unlock()
		InvalidateResourcePermissionsCache()
	})
	InvalidateResourcePermissionsCache()

	oldProbeDone := make(chan *PermissionCheckResult, 1)
	go func() {
		oldProbeDone <- CheckResourcePermissions(context.Background())
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("old cluster probe never started")
	}

	// The context switch: invalidate while the old probe is still in flight.
	InvalidateResourcePermissionsCache()
	close(release)

	select {
	case <-oldProbeDone:
	case <-time.After(10 * time.Second):
		t.Fatal("old probe did not finish")
	}

	if got := GetCachedPermissionResult(); got != nil {
		t.Fatalf("stale in-flight probe republished the previous cluster's result after invalidation (Pods=%v)", got.Perms.Pods)
	}

	// New cluster: everything denied — the opposite of the old cluster's
	// all-allowed shape, so a stale cache hit is unmistakable.
	clientMu.Lock()
	dynamicClient = fakeDyn(t, func(schema.GroupVersionResource, string) bool { return false })
	clientMu.Unlock()

	fresh := CheckResourcePermissions(context.Background())
	if fresh == nil {
		t.Fatal("fresh probe returned nil")
	}
	if fresh.Perms.Pods {
		t.Fatal("fresh probe served the previous cluster's cached result (Pods=true on an all-denied cluster)")
	}
	cached := GetCachedPermissionResult()
	if cached == nil {
		t.Fatal("fresh probe after invalidation must publish its result")
	}
	if cached.Perms.Pods {
		t.Fatal("cache holds the previous cluster's result (Pods=true on an all-denied cluster)")
	}
}
