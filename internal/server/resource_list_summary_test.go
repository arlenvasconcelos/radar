package server

// Tests for ?include=summary on typed resource lists (issue #1303: the 25k
// guard exists because full-object list payloads melt the browser at scale;
// the summary projection is what makes lifting it viable).
//
// The ratio assertion is deliberately machine-independent: it compares the
// summary payload against the full payload fetched in the same test run,
// never against absolute byte budgets.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const summaryFixtureNS = "summary-fixture"

// makeHeavyPod builds a pod with the field bulk real workloads carry: env,
// volume mounts, probes, tolerations, affinity, fat annotations. The summary
// projection must shed all of it; the table columns read none of it.
func makeHeavyPod(name string) *corev1.Pod {
	envs := make([]corev1.EnvVar, 0, 25)
	for i := 0; i < 20; i++ {
		envs = append(envs, corev1.EnvVar{
			Name:  fmt.Sprintf("APP_CONFIG_SETTING_%d", i),
			Value: fmt.Sprintf("value-for-setting-%d-with-some-realistic-length", i),
		})
	}
	for i := 0; i < 5; i++ {
		envs = append(envs, corev1.EnvVar{
			Name: fmt.Sprintf("SECRET_REF_%d", i),
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "app-secrets"},
					Key:                  fmt.Sprintf("key-%d", i),
				},
			},
		})
	}

	mounts := make([]corev1.VolumeMount, 0, 6)
	for i := 0; i < 6; i++ {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      fmt.Sprintf("vol-%d", i),
			MountPath: fmt.Sprintf("/var/lib/app/mount-point-%d", i),
			ReadOnly:  i%2 == 0,
		})
	}

	bigAnnotation := ""
	for i := 0; i < 40; i++ {
		bigAnnotation += fmt.Sprintf(`{"container":"sidecar-%d","state":"synced","detail":"connection established"},`, i)
	}

	probe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt(8080)},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       15,
		TimeoutSeconds:      3,
		FailureThreshold:    3,
	}

	started := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         summaryFixtureNS,
			UID:               types.UID("uid-" + name),
			CreationTimestamp: metav1.NewTime(time.Now().Add(-6 * time.Hour)),
			Labels: map[string]string{
				"app":                          "heavy-app",
				"app.kubernetes.io/name":       "heavy-app",
				"app.kubernetes.io/instance":   "heavy-app-prod",
				"app.kubernetes.io/version":    "1.42.0",
				"app.kubernetes.io/managed-by": "Helm",
				"pod-template-hash":            "7f9c6bd54",
			},
			Annotations: map[string]string{
				"sidecar.example.io/status": bigAnnotation,
				"checksum/config":           "3f8a9b2c1d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a",
				"kubectl.kubernetes.io/default-container": "main",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "heavy-app-7f9c6bd54", UID: "rs-uid-heavy",
			}},
		},
		Spec: corev1.PodSpec{
			NodeName:           "node-1",
			ServiceAccountName: "heavy-app-sa",
			NodeSelector:       map[string]string{"kubernetes.io/os": "linux", "workload-class": "general"},
			Tolerations: []corev1.Toleration{
				{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
				{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
				{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "general", Effect: corev1.TaintEffectNoSchedule},
			},
			Affinity: &corev1.Affinity{
				PodAntiAffinity: &corev1.PodAntiAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
						Weight: 100,
						PodAffinityTerm: corev1.PodAffinityTerm{
							LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "heavy-app"}},
							TopologyKey:   "kubernetes.io/hostname",
						},
					}},
				},
			},
			Volumes: []corev1.Volume{
				{Name: "vol-0", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}}},
				{Name: "vol-1", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "app-secrets"}}},
				{Name: "vol-2", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "vol-3", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "extra-config"}}}},
				{Name: "vol-4", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "vol-5", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "tls-certs"}}},
			},
			Containers: []corev1.Container{{
				Name:           "main",
				Image:          "registry.example.com/team/heavy-app:1.42.0",
				Env:            envs,
				VolumeMounts:   mounts,
				LivenessProbe:  probe,
				ReadinessProbe: probe,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.42.7.13",
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "main",
				Ready:        true,
				RestartCount: 2,
				Image:        "registry.example.com/team/heavy-app:1.42.0",
				State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{StartedAt: started},
				},
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 137, Reason: "OOMKilled",
						StartedAt:  metav1.NewTime(started.Add(-3 * time.Hour)),
						FinishedAt: metav1.NewTime(started.Add(-1 * time.Minute)),
					},
				},
			}},
		},
	}
}

// seedSummaryFixturePods creates n heavy pods and waits for the shared
// informer cache to see all of them. Returns a cleanup that deletes them and
// waits until the cache is clean again, so later tests in this package see
// exactly the TestMain-seeded state.
func seedSummaryFixturePods(t *testing.T, n int) func() {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		pod := makeHeavyPod(fmt.Sprintf("heavy-pod-%03d", i))
		if _, err := testFakeClient.CoreV1().Pods(summaryFixtureNS).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create fixture pod: %v", err)
		}
	}
	waitForFixturePodCount(t, n)
	return func() {
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("heavy-pod-%03d", i)
			if err := testFakeClient.CoreV1().Pods(summaryFixtureNS).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
				t.Errorf("delete fixture pod %s: %v", name, err)
			}
		}
		waitForFixturePodCount(t, 0)
	}
}

func waitForFixturePodCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp := get(t, "/api/resources/pods?namespaces="+summaryFixtureNS)
		var items []map[string]any
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err == nil && resp.StatusCode == http.StatusOK {
			if json.Unmarshal(body, &items) == nil && len(items) == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("informer cache never reached %d fixture pods (last saw %d)", want, len(items))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func fetchRawList(t *testing.T, path string) ([]byte, []map[string]any) {
	t.Helper()
	resp := get(t, path)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d: %s", path, resp.StatusCode, body)
	}
	var items []map[string]any
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return body, items
}

func nested(m map[string]any, path ...string) (any, bool) {
	var cur any = m
	for _, p := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// TestPodListSummaryProjection is the contract + payload test for
// ?include=summary on the typed pod list.
//
// Contract half: every field the resources table reads (columns, sort,
// filter, status badge, container squares — see k8s-ui resource-utils.ts and
// ResourcesView cell renderers) must survive in the summary.
// Payload half: the bulk the table never reads (env, volumes, probes,
// affinity, tolerations, fat annotations) must be gone, and the summary
// payload must be at least 4x smaller than the full payload for the same
// pods.
func TestPodListSummaryProjection(t *testing.T) {
	const n = 150
	cleanup := seedSummaryFixturePods(t, n)
	defer cleanup()

	fullBody, fullItems := fetchRawList(t, "/api/resources/pods?namespaces="+summaryFixtureNS)
	summaryBody, sumItems := fetchRawList(t, "/api/resources/pods?namespaces="+summaryFixtureNS+"&include=summary")

	if len(fullItems) != n || len(sumItems) != n {
		t.Fatalf("expected %d items in both lists, got full=%d summary=%d", n, len(fullItems), len(sumItems))
	}
	t.Logf("baseline: full=%d bytes (%d/pod), summary=%d bytes (%d/pod)",
		len(fullBody), len(fullBody)/n, len(summaryBody), len(summaryBody)/n)

	item := sumItems[0]

	// --- Contract: fields the table reads must be present ---
	mustHave := [][]string{
		{"kind"},
		{"apiVersion"},
		{"metadata", "name"},
		{"metadata", "namespace"},
		{"metadata", "creationTimestamp"},
		{"metadata", "labels"},
		{"metadata", "ownerReferences"},
		{"spec", "nodeName"},
		{"status", "phase"},
		{"status", "podIP"},
		{"status", "conditions"},
		{"status", "containerStatuses"},
	}
	for _, path := range mustHave {
		if _, ok := nested(item, path...); !ok {
			t.Errorf("summary item missing table-contract field %v", path)
		}
	}

	// Container statuses must keep the fields the container-squares column and
	// restart counters read: name, ready, restartCount, state, lastState.
	statuses, _ := nested(item, "status", "containerStatuses")
	statusList, ok := statuses.([]any)
	if !ok || len(statusList) == 0 {
		t.Fatalf("summary containerStatuses missing or empty: %T", statuses)
	}
	cs, _ := statusList[0].(map[string]any)
	for _, f := range []string{"name", "ready", "restartCount", "state", "lastState"} {
		if _, ok := cs[f]; !ok {
			t.Errorf("summary containerStatuses[0] missing %q", f)
		}
	}
	// spec.containers must keep name (metrics fallback) and resources (GPU
	// count + requests/limits context in the cpu/memory cells).
	containers, _ := nested(item, "spec", "containers")
	containerList, ok := containers.([]any)
	if !ok || len(containerList) == 0 {
		t.Fatalf("summary spec.containers missing or empty: %T", containers)
	}
	c0, _ := containerList[0].(map[string]any)
	if _, ok := c0["name"]; !ok {
		t.Errorf("summary spec.containers[0] missing name")
	}
	if _, ok := c0["resources"]; !ok {
		t.Errorf("summary spec.containers[0] missing resources")
	}
	// The default-container annotation drives exec/logs container selection.
	if _, ok := nested(item, "metadata", "annotations", "kubectl.kubernetes.io/default-container"); !ok {
		t.Errorf("summary must keep the kubectl.kubernetes.io/default-container annotation")
	}

	// --- Payload: bulk the table never reads must be gone ---
	mustNotHave := [][]string{
		{"spec", "volumes"},
		{"spec", "tolerations"},
		{"spec", "affinity"},
		{"spec", "nodeSelector"},
		{"metadata", "annotations", "sidecar.example.io/status"},
	}
	for _, path := range mustNotHave {
		if _, ok := nested(item, path...); ok {
			t.Errorf("summary item still carries heavy field %v", path)
		}
	}
	if _, ok := c0["env"]; ok {
		t.Errorf("summary spec.containers[0] still carries env")
	}
	if _, ok := c0["volumeMounts"]; ok {
		t.Errorf("summary spec.containers[0] still carries volumeMounts")
	}
	if _, ok := c0["livenessProbe"]; ok {
		t.Errorf("summary spec.containers[0] still carries livenessProbe")
	}

	// --- Ratio: the point of the projection ---
	if ratio := float64(len(fullBody)) / float64(len(summaryBody)); ratio < 4 {
		t.Errorf("summary payload only %.1fx smaller than full (want >= 4x): full=%d summary=%d",
			ratio, len(fullBody), len(summaryBody))
	}
}
