package audit

import (
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// TestCollectInputDeferredPendingLandsInMissingInputs pins the "couldn't check ≠
// passing" rule for the deferred kinds whose lister stays non-nil while their
// store warms (Ingresses, Jobs, CronJobs). listNamespaced can't tell an empty
// store from an unsynced one, so without the IsDeferredPending gate a scan run
// during warmup reports a clean bill of health on every Ingress/Job/CronJob
// check instead of admitting it couldn't look.
func TestCollectInputDeferredPendingLandsInMissingInputs(t *testing.T) {
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)

	client := fake.NewClientset()
	blockList := func(resource string, empty runtime.Object) {
		client.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
			<-release
			return true, empty, nil
		})
	}
	blockList("ingresses", &networkingv1.IngressList{})
	blockList("jobs", &batchv1.JobList{})
	blockList("cronjobs", &batchv1.CronJobList{})

	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			k8score.Pods:        true,
			k8score.Deployments: true,
			k8score.Ingresses:   true,
			k8score.Jobs:        true,
			k8score.CronJobs:    true,
		},
		DeferredTypes: map[string]bool{
			k8score.Ingresses: true,
			k8score.Jobs:      true,
			k8score.CronJobs:  true,
		},
		// Must outlast the assertions below: once the deadline fires the cache
		// gives up on stragglers and IsDeferredPending flips to false.
		DeferredSyncTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}

	for _, kind := range []string{k8score.Ingresses, k8score.Jobs, k8score.CronJobs} {
		if !cache.IsDeferredPending(kind) {
			t.Fatalf("%s should be deferred-pending while its LIST is blocked", kind)
		}
	}

	input := collectTypedInput(cache, nil)
	if input.Ingresses != nil || input.Jobs != nil || input.CronJobs != nil {
		t.Errorf("warming inputs must stay nil, got ingresses=%d jobs=%d cronjobs=%d",
			len(input.Ingresses), len(input.Jobs), len(input.CronJobs))
	}
	if input.Deployments == nil {
		t.Error("Deployments synced with the critical tier; its input must not be suppressed")
	}

	missing := map[string]bool{}
	for _, in := range bp.RunChecks(input).MissingInputs {
		missing[in] = true
	}
	for _, kind := range []string{"ingresses", "jobs", "cronjobs"} {
		if !missing[kind] {
			t.Errorf("%q absent from MissingInputs — a warming store would read as a passing scan", kind)
		}
	}

	releaseOnce()
	select {
	case <-core.DeferredDone():
	case <-time.After(30 * time.Second):
		t.Fatal("deferred sync never completed after unblocking LIST")
	}

	input = collectTypedInput(cache, nil)
	if input.Ingresses == nil || input.Jobs == nil || input.CronJobs == nil {
		t.Fatalf("after deferred sync the inputs must be non-nil (empty is fine): ingresses=%v jobs=%v cronjobs=%v",
			input.Ingresses, input.Jobs, input.CronJobs)
	}
	for _, in := range bp.RunChecks(input).MissingInputs {
		if in == "ingresses" || in == "jobs" || in == "cronjobs" {
			t.Errorf("%q must not be reported missing once its informer synced", in)
		}
	}
}
