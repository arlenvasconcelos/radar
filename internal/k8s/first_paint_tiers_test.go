package k8s

import "testing"

// TestFirstPaintTierAssignments pins the production tier split. The deferred
// tier exists so kinds that can carry tens of thousands of objects on large
// clusters don't stream their initial LISTs during the window that decides
// time to first paint; the minimal set is what the home dashboard needs to
// render at all. Moving a kind across this boundary is a first-paint latency
// decision, not a housekeeping edit — this test makes that move deliberate.
func TestFirstPaintTierAssignments(t *testing.T) {
	for kind := range minimalFirstPaintSet {
		if deferredResources[kind] {
			t.Errorf("%s is in minimalFirstPaintSet but deferred — first paint would render without a kind the dashboard requires", kind)
		}
	}

	heavyAtScale := []string{
		"replicasets",
		"events",
		"secrets",
		"configmaps",
		"ingresses",
		"jobs",
		"cronjobs",
	}
	for _, kind := range heavyAtScale {
		if !deferredResources[kind] {
			t.Errorf("%s is not deferred — at large-cluster scale its initial LIST would stream during the first-paint window", kind)
		}
	}
}
