package k8score

import (
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// heavySecondaryKinds are kinds that can carry tens of thousands of objects
// on large clusters while contributing nothing to the first dashboard render.
// Their initial LISTs stream over the same HTTP/2 connection as the Pod LIST,
// so whether they sit in the critical or deferred tier decides how long
// NewResourceCache holds first paint hostage on such clusters.
var heavySecondaryKinds = []string{Ingresses, Jobs, CronJobs}

// newHeavySecondaryListClient returns a fake clientset where every LIST of a
// heavy secondary kind blocks until release is closed — a stand-in for the
// multi-second initial LISTs those kinds produce at large-cluster scale. The
// kinds the dashboard renders first stay small and list instantly.
func newHeavySecondaryListClient(release <-chan struct{}) *fake.Clientset {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "prod"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "prod"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "prod"}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "prod"}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"}},
	)
	for _, resource := range heavySecondaryKinds {
		client.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
			select {
			case <-release:
			case <-time.After(30 * time.Second):
				// Backstop so a regression cannot wedge the test binary; the
				// assertions below fail long before this fires.
			}
			return false, nil, nil // fall through to the tracker's default reactor
		})
	}
	return client
}

func firstPaintTestConfig(client *fake.Clientset, deferred map[string]bool) CacheConfig {
	return CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods:        true,
			Services:    true,
			Deployments: true,
			Nodes:       true,
			Namespaces:  true,
			Ingresses:   true,
			Jobs:        true,
			CronJobs:    true,
		},
		DeferredTypes: deferred,
		// PatienceWindow/MinimalSet stay off: the fallback only rescues first
		// paint when the minimal set itself syncs fast, and on large clusters
		// the heavy LISTs starve the Pod LIST sharing their connection — a
		// contention a fake clientset cannot reproduce. What this test pins
		// is the tier contract: which kinds gate the Phase 1 return at all.
		SyncTimeout: 10 * time.Second,
	}
}

// TestFirstPaintTierGatingUnderHeavyLists simulates the large-cluster shape
// where Ingress/Job/CronJob LISTs take multi-second (blocked here) while the
// first-render kinds list instantly, and pins which tier gates first paint.
func TestFirstPaintTierGatingUnderHeavyLists(t *testing.T) {
	t.Run("heavy kinds critical: first paint held until their LISTs complete", func(t *testing.T) {
		release := make(chan struct{})
		var once sync.Once
		releaseHeavyLists := func() { once.Do(func() { close(release) }) }
		t.Cleanup(releaseHeavyLists)

		client := newHeavySecondaryListClient(release)

		type result struct {
			rc  *ResourceCache
			err error
		}
		done := make(chan result, 1)
		go func() {
			rc, err := NewResourceCache(firstPaintTestConfig(client, nil))
			done <- result{rc, err}
		}()

		select {
		case res := <-done:
			if res.rc != nil {
				res.rc.Stop()
			}
			t.Fatalf("NewResourceCache returned while heavy critical LISTs were still streaming (err=%v) — the critical gate no longer waits for its informers", res.err)
		case <-time.After(750 * time.Millisecond):
			// Held, as expected: first paint is hostage to the heavy LISTs.
		}

		releaseHeavyLists()
		select {
		case res := <-done:
			if res.err != nil {
				t.Fatalf("NewResourceCache failed after release: %v", res.err)
			}
			res.rc.Stop()
		case <-time.After(10 * time.Second):
			t.Fatal("NewResourceCache did not return after heavy LISTs were released")
		}
	})

	t.Run("heavy kinds deferred: first paint returns while their LISTs still stream", func(t *testing.T) {
		release := make(chan struct{})
		var once sync.Once
		releaseHeavyLists := func() { once.Do(func() { close(release) }) }
		t.Cleanup(releaseHeavyLists)

		client := newHeavySecondaryListClient(release)

		deferred := make(map[string]bool, len(heavySecondaryKinds))
		for _, kind := range heavySecondaryKinds {
			deferred[kind] = true
		}

		start := time.Now()
		rc, err := NewResourceCache(firstPaintTestConfig(client, deferred))
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("NewResourceCache failed: %v", err)
		}
		defer rc.Stop()
		defer releaseHeavyLists()

		// release is still open here, so the heavy LISTs are provably still
		// in flight: the return above cannot have waited on them.
		if len(rc.promotedKinds) != 0 {
			t.Fatalf("first paint only returned because SyncTimeout promoted stuck critical informers (%v) — deferred tiering is not keeping heavy kinds off the gate", rc.promotedKinds)
		}
		if elapsed >= 5*time.Second {
			t.Fatalf("first paint took %v with heavy kinds deferred; expected near-instant return", elapsed)
		}
	})
}
