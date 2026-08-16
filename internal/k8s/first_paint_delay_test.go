package k8s

import "testing"

func TestFirstPaintDelayStartPins(t *testing.T) {
	if minimalFirstPaintSet["pods"] {
		t.Fatal("pods must not gate first paint — they are the largest LIST")
	}
	for _, key := range []string{"services", "deployments", "nodes", "namespaces"} {
		if !minimalFirstPaintSet[key] {
			t.Errorf("minimalFirstPaintSet missing %s", key)
		}
	}
	if !delayStartFirstPaint["pods"] {
		t.Fatal("pods must be DelayStart so the 29k LIST does not start with wave 1")
	}
	for key := range delayStartFirstPaint {
		if minimalFirstPaintSet[key] {
			t.Errorf("DelayStart %q is also in MinimalSet — first paint would deadlock", key)
		}
	}
}
