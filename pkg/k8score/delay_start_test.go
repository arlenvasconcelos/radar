package k8score

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// firstPaintEnabledTypes is the home-dashboard kind set with Pods still
// enabled but not gating paint: Services/Deployments/Nodes/Namespaces
// can render while the Pod LIST is still in flight.
func firstPaintEnabledTypes() map[string]bool {
	return map[string]bool{
		Pods:        true,
		Services:    true,
		Deployments: true,
		Nodes:       true,
		Namespaces:  true,
	}
}

func firstPaintMinimalSet() map[string]bool {
	return map[string]bool{
		Services:    true,
		Deployments: true,
		Nodes:       true,
		Namespaces:  true,
	}
}

func holdList(hold <-chan struct{}) k8stesting.ReactionFunc {
	return func(k8stesting.Action) (bool, runtime.Object, error) {
		<-hold
		return false, nil, nil
	}
}

func countList(n *atomic.Int32) k8stesting.ReactionFunc {
	return func(k8stesting.Action) (bool, runtime.Object, error) {
		n.Add(1)
		return false, nil, nil
	}
}

// TestDelayStart_PodListWaitsForWave1: Service/Deployment/Node/Namespace
// LISTs are still in flight. The Pod LIST must not start until those
// wave-1 kinds unstick — otherwise it contends on the same HTTP/2
// connection and first paint waits on the largest kind.
func TestDelayStart_PodListWaitsForWave1(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)

	client := fake.NewSimpleClientset()
	hold := make(chan struct{})
	var podLists atomic.Int32

	for _, resource := range []string{"services", "deployments", "nodes", "namespaces"} {
		client.PrependReactor("list", resource, holdList(hold))
	}
	client.PrependReactor("list", "pods", countList(&podLists))

	done := make(chan *ResourceCache, 1)
	errCh := make(chan error, 1)
	go func() {
		rc, err := NewResourceCache(CacheConfig{
			Client:         client,
			ResourceTypes:  firstPaintEnabledTypes(),
			PatienceWindow: 200 * time.Millisecond,
			MinimalSet:     firstPaintMinimalSet(),
			DelayStart:     map[string]bool{Pods: true},
			SyncTimeout:    5 * time.Second,
		})
		if err != nil {
			errCh <- err
			return
		}
		done <- rc
	}()

	time.Sleep(300 * time.Millisecond)
	if n := podLists.Load(); n != 0 {
		t.Fatalf("Pod LIST started while wave 1 still blocked: %d calls", n)
	}

	close(hold)

	var rc *ResourceCache
	select {
	case rc = <-done:
	case err := <-errCh:
		t.Fatalf("NewResourceCache failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("NewResourceCache did not return after wave 1 released")
	}
	defer rc.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for podLists.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if podLists.Load() == 0 {
		t.Fatal("Pod LIST never started after wave 1 synced")
	}
}

// TestDelayStart_FirstPaintDoesNotWaitForPods: wave 1 is ready, Pod LIST
// is stuck. NewResourceCache must return after patience instead of
// blocking on Pods.
func TestDelayStart_FirstPaintDoesNotWaitForPods(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)

	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated stuck Pod LIST")
	})

	start := time.Now()
	rc, err := NewResourceCache(CacheConfig{
		Client:         client,
		ResourceTypes:  firstPaintEnabledTypes(),
		PatienceWindow: 200 * time.Millisecond,
		MinimalSet:     firstPaintMinimalSet(),
		DelayStart:     map[string]bool{Pods: true},
		SyncTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()
	elapsed := time.Since(start)

	if elapsed < 200*time.Millisecond {
		t.Errorf("expected return after patience (200ms), got %v", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("first paint waited on Pods; elapsed=%v", elapsed)
	}

	promoted := rc.PromotedKinds()
	if len(promoted) != 1 || promoted[0] != "Pod" {
		t.Errorf("expected Pod to be promoted as still-loading, got %v", promoted)
	}
	if rc.Services() == nil || rc.Deployments() == nil {
		t.Error("expected wave-1 listers available at first paint")
	}

	// Lister is isEnabled-gated — non-nil over an empty store. Handlers
	// must see pending so they return 503 instead of 404 / empty 200.
	if rc.Pods() == nil {
		t.Fatal("Pods() must stay non-nil while the delayed LIST is in flight")
	}
	if !rc.IsDeferredPending(Pods) {
		t.Fatal("promoted Pod LIST must be IsDeferredPending so REST/audit do not treat the empty store as truth")
	}
}

// TestDelayStart_FastPathNoPatienceRegression: small cluster, everything
// syncs. Delayed Pod LIST must start as soon as wave 1 is ready so
// all-critical can finish inside the patience window — otherwise healthy
// clusters wait 8s for nothing.
func TestDelayStart_FastPathNoPatienceRegression(t *testing.T) {
	client := fake.NewSimpleClientset()

	start := time.Now()
	rc, err := NewResourceCache(CacheConfig{
		Client:         client,
		ResourceTypes:  firstPaintEnabledTypes(),
		PatienceWindow: 2 * time.Second,
		MinimalSet:     firstPaintMinimalSet(),
		DelayStart:     map[string]bool{Pods: true},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("fast path waited on patience; elapsed=%v", elapsed)
	}
	if got := rc.PromotedKinds(); len(got) != 0 {
		t.Errorf("expected no promotion on fast path, got %v", got)
	}
	if rc.Pods() == nil {
		t.Error("expected Pods() lister after fast-path sync")
	}
	if rc.IsDeferredPending(Pods) {
		t.Error("Pods synced on the fast path; IsDeferredPending must be false")
	}
}

func TestDelayStart_IgnoredWhenAlsoInMinimalSet(t *testing.T) {
	client := fake.NewSimpleClientset()

	start := time.Now()
	rc, err := NewResourceCache(CacheConfig{
		Client:         client,
		ResourceTypes:  map[string]bool{Pods: true},
		PatienceWindow: 2 * time.Second,
		MinimalSet:     map[string]bool{Pods: true},
		DelayStart:     map[string]bool{Pods: true},
		SyncTimeout:    2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("DelayStart∩MinimalSet delayed Pods — first paint deadlocked until timeout; elapsed=%v", elapsed)
	}
	if rc.Pods() == nil {
		t.Error("expected Pods() lister")
	}
}
