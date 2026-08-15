package server

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	topology "github.com/skyhook-io/radar/pkg/topology"
)

// The topology worker exists so that a multi-second topology build never runs
// on the goroutine that drains the resource-change channel. These tests pin the
// two properties that make that true: requesting a broadcast never blocks the
// caller, and concurrent requests coalesce instead of queueing up builds.

// blockWorker occupies the topology worker with a build that only finishes when
// the returned release func is called, and reports when the worker has actually
// started. It stands in for a real large-cluster build without needing a census.
func blockWorker(t *testing.T, b *SSEBroadcaster) (release func()) {
	t.Helper()

	started := make(chan struct{})
	held := make(chan struct{})
	go func() {
		select {
		case <-b.topoTrigger:
			close(started)
			<-held
		case <-time.After(2 * time.Second):
			close(started)
		}
	}()

	b.requestTopologyBroadcast()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stand-in worker never picked up the trigger")
	}

	released := false
	return func() {
		if !released {
			released = true
			close(held)
		}
	}
}

func TestRequestTopologyBroadcastDoesNotBlockWhileBuildInFlight(t *testing.T) {
	b := NewSSEBroadcaster()
	release := blockWorker(t, b)
	defer release()

	// A build is now in flight. The change-consumer goroutine reaches this call
	// on every debounce fire; if it blocks here, changes stop being drained and
	// the resource-change channel overflows.
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		b.requestTopologyBroadcast()
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		// Generous bound: the point is "returns without waiting for the build",
		// not a latency budget. A blocking implementation waits indefinitely.
		if elapsed > 250*time.Millisecond {
			t.Errorf("requestTopologyBroadcast blocked for %v while a build was in flight; "+
				"the change consumer must never wait on a build", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("requestTopologyBroadcast blocked while a build was in flight; " +
			"the change consumer would stall and drop resource changes")
	}
}

func TestConcurrentTopologyRequestsCoalesce(t *testing.T) {
	b := NewSSEBroadcaster()
	release := blockWorker(t, b)
	defer release()

	// Every trigger source (debounce fire, context switch, CRD discovery,
	// connection state) can fire while a build runs. They must collapse into a
	// single pending request rather than each queueing another full build.
	for range 50 {
		b.requestTopologyBroadcast()
	}

	if got := len(b.topoTrigger); got > 1 {
		t.Errorf("pending topology requests = %d, want at most 1: concurrent triggers must coalesce", got)
	}
}

// A relationship read is answered from the cluster as it is now, not as it was.
// Owner edges and cascade previews are acted on — a stale answer is worse than a
// slow one — so a dirty cache is rebuilt on the reader's own goroutine before it
// returns.
func TestDirtyCacheReadRebuildsInlineRatherThanServingStale(t *testing.T) {
	if k8s.GetResourceCache() == nil {
		t.Fatal("package fixture cache missing")
	}

	b := NewSSEBroadcaster()
	// No worker running: the rebuild has to happen on this goroutine or not at all.
	stale := &topology.Topology{Nodes: []topology.Node{{ID: "stale"}}}
	b.cachedTopologyMu.Lock()
	b.cachedTopology = stale
	b.cachedTopologyDirty = true
	b.cachedTopologyMu.Unlock()

	got := b.GetCachedTopology()

	if got == stale {
		t.Error("dirty read returned the previous graph; a reader must rebuild before answering")
	}
	if got == nil {
		t.Fatal("dirty read returned nothing; the rebuild must produce a graph")
	}

	b.cachedTopologyMu.RLock()
	dirty := b.cachedTopologyDirty
	b.cachedTopologyMu.RUnlock()
	if dirty {
		t.Error("cache still dirty after an inline rebuild")
	}
}

// TestWarmupBroadcastMarksDirtyInsteadOfBuilding pins the rule that decides
// whether a dirty read can hit a cold cache: while deferred informers are still
// syncing, a broadcast cycle only flags the relationship cache, it does not
// build it. That is what leaves the cache stale for HTTP readers throughout
// warmup — on a large cluster that window is minutes long.
func TestWarmupBroadcastMarksDirtyInsteadOfBuilding(t *testing.T) {
	if k8s.GetResourceCache() == nil {
		t.Fatal("package fixture cache missing")
	}

	b := NewSSEBroadcaster()
	go b.run()
	t.Cleanup(b.Stop)

	// broadcastTopologyUpdate returns early with no clients, so subscribe one.
	ch := b.Subscribe(nil, "resources", nil, nil)
	t.Cleanup(func() { b.Unsubscribe(ch) })
	for range 100 {
		if b.ClientCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b.ClientCount() == 0 {
		t.Fatal("client never registered")
	}

	// Warming up: warmupDone is open.
	b.broadcastTopologyUpdate()

	b.cachedTopologyMu.RLock()
	dirtyDuringWarmup := b.cachedTopologyDirty
	topoDuringWarmup := b.cachedTopology
	b.cachedTopologyMu.RUnlock()

	if !dirtyDuringWarmup {
		t.Error("during warmup the cache was not marked dirty; a broadcast must flag it for later refresh")
	}
	if topoDuringWarmup != nil {
		t.Error("during warmup the relationship cache was built; warmup is supposed to skip that build")
	}

	// Warmup complete.
	b.watchMu.Lock()
	close(b.warmupDone)
	b.watchMu.Unlock()

	b.broadcastTopologyUpdate()

	b.cachedTopologyMu.RLock()
	topoAfterWarmup := b.cachedTopology
	dirtyAfterWarmup := b.cachedTopologyDirty
	b.cachedTopologyMu.RUnlock()

	if topoAfterWarmup == nil {
		t.Error("after warmup the relationship cache was still not built")
	}
	if dirtyAfterWarmup {
		t.Error("after warmup the cache is still dirty; a completed build must clear the flag")
	}
}

// A cluster that stops changing stops rebuilding: the debounce timer is armed by
// incoming changes, never by a clock. A clean graph can therefore sit untouched
// for hours and still describe the cluster exactly, because nothing happened for
// it to miss — so a read must not pay a build for it.
func TestCleanCacheIsServedWithoutRebuilding(t *testing.T) {
	if k8s.GetResourceCache() == nil {
		t.Fatal("package fixture cache missing")
	}

	b := NewSSEBroadcaster()
	clean := &topology.Topology{Nodes: []topology.Node{{ID: "clean"}}}
	b.cachedTopologyMu.Lock()
	b.cachedTopology = clean
	b.cachedTopologyDirty = false
	b.cachedTopologyMu.Unlock()

	if got := b.GetCachedTopology(); got != clean {
		t.Errorf("GetCachedTopology returned %v, want the cached graph untouched", got)
	}
}

// A build reads the process-global caches for seconds. If a kubeconfig context
// switch lands while one is in flight, everything it produced describes the
// cluster the user just left — so it must be dropped rather than cached, even
// though it finished after the switch.
func TestBuildFromThePreviousClusterIsNotCached(t *testing.T) {
	b := NewSSEBroadcaster()

	epoch := b.topoEpoch.Load() // what a build in flight would have captured
	b.topoEpoch.Add(1)          // the switch lands

	previousCluster := &topology.Topology{Nodes: []topology.Node{{ID: "old-cluster"}}}
	if b.updateCachedTopology(previousCluster, epoch) {
		t.Error("a build started before the context switch reported itself as cached")
	}

	b.cachedTopologyMu.RLock()
	cached := b.cachedTopology
	b.cachedTopologyMu.RUnlock()
	if cached != nil {
		t.Errorf("cached %v after a context switch; relationship lookups would answer from the previous cluster", cached)
	}

	// The new cluster's build still lands.
	newCluster := &topology.Topology{Nodes: []topology.Node{{ID: "new-cluster"}}}
	if !b.updateCachedTopology(newCluster, b.topoEpoch.Load()) {
		t.Fatal("a build started after the switch was rejected too")
	}
}

// Same rule on the wire: a cycle that started against the previous cluster must
// not reach clients, or the UI shows the old cluster's graph after it has
// already been told the context changed.
func TestBroadcastFromThePreviousClusterIsNotPublished(t *testing.T) {
	if k8s.GetResourceCache() == nil {
		t.Fatal("package fixture cache missing")
	}

	b := NewSSEBroadcaster()
	go b.run()
	t.Cleanup(b.Stop)

	ch := b.Subscribe(nil, "resources", nil, nil)
	t.Cleanup(func() { b.Unsubscribe(ch) })
	for range 100 {
		if b.ClientCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b.ClientCount() == 0 {
		t.Fatal("client never registered")
	}

	// Control: with no switch, this same cycle does publish. Without it the
	// assertion below would hold for a broadcaster that never publishes at all.
	b.broadcastTopologyUpdate()
	if !drainForTopologyFrame(ch) {
		t.Fatal("no topology frame published without a context switch; the test below would prove nothing")
	}

	// Hold the client registry so the cycle is provably mid-flight — it has
	// captured its epoch and cannot yet have published — when the switch lands.
	b.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.broadcastTopologyUpdate()
	}()
	time.Sleep(50 * time.Millisecond)
	b.topoEpoch.Add(1) // context switch
	b.mu.Unlock()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("broadcast never finished")
	}

	if drainForTopologyFrame(ch) {
		t.Fatal("published a topology frame built against the previous cluster; " +
			"the UI would render it after context_changed")
	}
}

func drainForTopologyFrame(ch chan SSEEvent) bool {
	found := false
	for {
		select {
		case event := <-ch:
			if event.Event == "topology" {
				found = true
			}
		default:
			return found
		}
	}
}

func TestTopologyWorkerStopsWithBroadcaster(t *testing.T) {
	b := NewSSEBroadcaster()

	stopped := make(chan struct{})
	go func() {
		b.topologyWorker()
		close(stopped)
	}()

	b.Stop()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("topologyWorker did not exit after Stop; goroutine leaked")
	}
}
