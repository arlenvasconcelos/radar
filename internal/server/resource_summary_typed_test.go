package server

import (
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func heavyPodTemplate() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "registry.example.com/app:1",
				Env:   []corev1.EnvVar{{Name: "A", Value: "B"}, {Name: "C", Value: "D"}},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "config", MountPath: "/etc/app"},
				},
			}},
			Volumes: []corev1.Volume{{Name: "config"}},
		},
	}
}

// The whole point of the ReplicaSet/Job projections: shed the embedded pod
// template (~7KB per object, 54% of ReplicaSets on large clusters are dead
// revisions) while keeping the replica/condition fields the table reads.
func TestReplicaSetSummaryDropsTemplate(t *testing.T) {
	replicas := int32(3)
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-7f9c6bd54", Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "app"}},
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "app"}},
			Template: heavyPodTemplate(),
		},
		Status: appsv1.ReplicaSetStatus{Replicas: 3, ReadyReplicas: 3, AvailableReplicas: 3},
	}
	m := marshalToMap(t, replicaSetToSummary(rs))
	spec := m["spec"].(map[string]any)
	if _, ok := spec["template"]; ok {
		t.Fatal("ReplicaSet summary must not carry spec.template")
	}
	if spec["replicas"] != float64(3) {
		t.Errorf("spec.replicas lost: %v", spec["replicas"])
	}
	status := m["status"].(map[string]any)
	if status["readyReplicas"] != float64(3) {
		t.Errorf("status.readyReplicas lost: %v", status["readyReplicas"])
	}
	if _, ok := m["metadata"].(map[string]any)["ownerReferences"]; !ok {
		t.Error("ownerReferences lost (owner column + owner filter read them)")
	}
}

func TestJobSummaryDropsTemplateKeepsStatus(t *testing.T) {
	completions := int32(5)
	suspend := true
	start := metav1.Now()
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "batch-1", Namespace: "default"},
		Spec: batchv1.JobSpec{
			Completions: &completions,
			Suspend:     &suspend,
			Template:    heavyPodTemplate(),
		},
		Status: batchv1.JobStatus{
			Succeeded: 4, Active: 1, StartTime: &start,
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionFalse}},
		},
	}
	m := marshalToMap(t, jobToSummary(job))
	spec := m["spec"].(map[string]any)
	if _, ok := spec["template"]; ok {
		t.Fatal("Job summary must not carry spec.template")
	}
	if spec["completions"] != float64(5) || spec["suspend"] != true {
		t.Errorf("spec completions/suspend lost: %v", spec)
	}
	status := m["status"].(map[string]any)
	for _, f := range []string{"succeeded", "active", "startTime", "conditions"} {
		if _, ok := status[f]; !ok {
			t.Errorf("status.%s lost (status badge / completions / duration columns read it)", f)
		}
	}
}

func TestServiceSummaryKeepsColumnsDropsBulk(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api", Namespace: "default",
			Annotations: map[string]string{"service.beta.kubernetes.io/aws-load-balancer-attributes": "x"},
		},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeLoadBalancer,
			ClusterIP: "10.0.0.10",
			Selector:  map[string]string{"app": "api"},
			Ports:     []corev1.ServicePort{{Port: 443, Protocol: corev1.ProtocolTCP}},
			// Fields the table never reads:
			SessionAffinity:       corev1.ServiceAffinityClientIP,
			InternalTrafficPolicy: ptrTo(corev1.ServiceInternalTrafficPolicyCluster),
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}},
			},
		},
	}
	m := marshalToMap(t, serviceToSummary(svc))
	spec := m["spec"].(map[string]any)
	for _, f := range []string{"type", "clusterIP", "selector", "ports"} {
		if _, ok := spec[f]; !ok {
			t.Errorf("spec.%s lost (type/endpoints/selector/ports columns read it)", f)
		}
	}
	if _, ok := spec["sessionAffinity"]; ok {
		t.Error("spec.sessionAffinity should be dropped")
	}
	if _, ok := m["metadata"].(map[string]any)["annotations"]; ok {
		t.Error("service annotations should be dropped")
	}
	lb, _ := m["status"].(map[string]any)["loadBalancer"].(map[string]any)
	if _, ok := lb["ingress"]; !ok {
		t.Error("status.loadBalancer.ingress lost (externalIP column reads it)")
	}
}

func TestEventSummaryKeepsListFields(t *testing.T) {
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p1", Namespace: "default"},
		Reason:         "BackOff",
		Message:        "Back-off restarting failed container",
		Type:           "Warning",
		Count:          7,
		LastTimestamp:  metav1.Now(),
	}
	m := marshalToMap(t, eventToSummary(ev))
	for _, f := range []string{"involvedObject", "reason", "message", "type", "count", "lastTimestamp"} {
		if _, ok := m[f]; !ok {
			t.Errorf("event summary lost %s (event columns read it)", f)
		}
	}
}

func ptrTo[T any](v T) *T { return &v }
