package server

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Typed-list counterpart of resource_summary.go's prune profiles: the dynamic
// path subtracts heavy subtrees from unstructured objects, but typed-cache
// lists (pods, services, jobs, ...) marshal Go structs, so their summary is a
// PROJECTION — per-kind structs that keep exactly the fields the resources
// table renders (columns, sort, filter, status badge; see k8s-ui
// resource-utils.ts and the ResourcesView cell renderers) and shed the bulk
// it never reads (env, volumes, probes, affinity, pod templates, fat
// annotations). The detail drawer fetches the full object through its own
// query, so nothing below the list view depends on the dropped fields.
//
// Contract: resource_list_summary_test.go pins both halves — fields the
// table reads must survive, heavy fields must be gone, and the summary must
// be materially smaller than the full payload. When a table column gains a
// new field, extend the projection AND the contract test together.
//
// JSON field names must mirror the K8s wire shape exactly — the frontend
// reads summary and full objects through the same accessors.

type summaryMeta struct {
	Name              string                  `json:"name"`
	Namespace         string                  `json:"namespace,omitempty"`
	UID               types.UID               `json:"uid,omitempty"`
	CreationTimestamp metav1.Time             `json:"creationTimestamp"`
	DeletionTimestamp *metav1.Time            `json:"deletionTimestamp,omitempty"`
	Labels            map[string]string       `json:"labels,omitempty"`
	Annotations       map[string]string       `json:"annotations,omitempty"`
	OwnerReferences   []metav1.OwnerReference `json:"ownerReferences,omitempty"`
}

// podSummaryAnnotations lists the only annotations the list UI reads (exec /
// logs default-container selection). Everything else — sidecar status blobs,
// checksums, last-applied leftovers — is dead weight at list scale.
var podSummaryAnnotations = []string{"kubectl.kubernetes.io/default-container"}

func projectMeta(m metav1.ObjectMeta, keepAnnotations []string) summaryMeta {
	out := summaryMeta{
		Name:              m.Name,
		Namespace:         m.Namespace,
		UID:               m.UID,
		CreationTimestamp: m.CreationTimestamp,
		DeletionTimestamp: m.DeletionTimestamp,
		Labels:            m.Labels,
		OwnerReferences:   m.OwnerReferences,
	}
	for _, key := range keepAnnotations {
		if v, ok := m.Annotations[key]; ok {
			if out.Annotations == nil {
				out.Annotations = make(map[string]string, len(keepAnnotations))
			}
			out.Annotations[key] = v
		}
	}
	return out
}

// --- Pod ---

type podSummary struct {
	Kind       string           `json:"kind"`
	APIVersion string           `json:"apiVersion"`
	Metadata   summaryMeta      `json:"metadata"`
	Spec       podSummarySpec   `json:"spec"`
	Status     podSummaryStatus `json:"status"`
}

type podSummarySpec struct {
	NodeName       string                `json:"nodeName,omitempty"`
	Containers     []podSummaryContainer `json:"containers,omitempty"`
	InitContainers []podSummaryContainer `json:"initContainers,omitempty"`
}

// Name feeds the cpu/memory cells' single-container fallback and the
// default-container pick; Resources feeds the GPU column and request/limit
// context in the metrics tooltips. RestartPolicy is what separates a native
// sidecar from an init container that keeps dying — without it a healthy
// restarted sidecar reads as unhealthy.
type podSummaryContainer struct {
	Name          string                         `json:"name"`
	Image         string                         `json:"image,omitempty"`
	Resources     corev1.ResourceRequirements    `json:"resources,omitempty"`
	RestartPolicy *corev1.ContainerRestartPolicy `json:"restartPolicy,omitempty"`
}

// Container statuses are projected too: modern kubelets attach volumeMounts,
// allocatedResources and user info to each status entry, none of which the
// container-squares column reads.
type podSummaryContainerStatus struct {
	Name         string                `json:"name"`
	Ready        bool                  `json:"ready"`
	RestartCount int32                 `json:"restartCount"`
	Started      *bool                 `json:"started,omitempty"`
	State        corev1.ContainerState `json:"state,omitempty"`
	LastState    corev1.ContainerState `json:"lastState,omitempty"`
}

// Message carries the cause the problem rows render as detail — the eviction
// or failure reason is the whole point of those rows.
type podSummaryStatus struct {
	Phase                 corev1.PodPhase             `json:"phase,omitempty"`
	Reason                string                      `json:"reason,omitempty"`
	Message               string                      `json:"message,omitempty"`
	PodIP                 string                      `json:"podIP,omitempty"`
	Conditions            []corev1.PodCondition       `json:"conditions,omitempty"`
	ContainerStatuses     []podSummaryContainerStatus `json:"containerStatuses,omitempty"`
	InitContainerStatuses []podSummaryContainerStatus `json:"initContainerStatuses,omitempty"`
}

func projectContainers(containers []corev1.Container) []podSummaryContainer {
	if len(containers) == 0 {
		return nil
	}
	out := make([]podSummaryContainer, len(containers))
	for i, c := range containers {
		out[i] = podSummaryContainer{
			Name:          c.Name,
			Image:         c.Image,
			Resources:     c.Resources,
			RestartPolicy: c.RestartPolicy,
		}
	}
	return out
}

func projectContainerStatuses(statuses []corev1.ContainerStatus) []podSummaryContainerStatus {
	if len(statuses) == 0 {
		return nil
	}
	out := make([]podSummaryContainerStatus, len(statuses))
	for i, cs := range statuses {
		out[i] = podSummaryContainerStatus{
			Name:         cs.Name,
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
			Started:      cs.Started,
			State:        cs.State,
			LastState:    cs.LastTerminationState,
		}
	}
	return out
}

func podToSummary(p *corev1.Pod) podSummary {
	return podSummary{
		Kind: "Pod", APIVersion: "v1",
		Metadata: projectMeta(p.ObjectMeta, podSummaryAnnotations),
		Spec: podSummarySpec{
			NodeName:       p.Spec.NodeName,
			Containers:     projectContainers(p.Spec.Containers),
			InitContainers: projectContainers(p.Spec.InitContainers),
		},
		Status: podSummaryStatus{
			Phase:                 p.Status.Phase,
			Reason:                p.Status.Reason,
			Message:               p.Status.Message,
			PodIP:                 p.Status.PodIP,
			Conditions:            p.Status.Conditions,
			ContainerStatuses:     projectContainerStatuses(p.Status.ContainerStatuses),
			InitContainerStatuses: projectContainerStatuses(p.Status.InitContainerStatuses),
		},
	}
}

// --- Service ---

type serviceSummary struct {
	Kind       string               `json:"kind"`
	APIVersion string               `json:"apiVersion"`
	Metadata   summaryMeta          `json:"metadata"`
	Spec       serviceSummarySpec   `json:"spec"`
	Status     serviceSummaryStatus `json:"status"`
}

type serviceSummarySpec struct {
	Type         corev1.ServiceType   `json:"type,omitempty"`
	ClusterIP    string               `json:"clusterIP,omitempty"`
	Ports        []corev1.ServicePort `json:"ports,omitempty"`
	Selector     map[string]string    `json:"selector,omitempty"`
	ExternalIPs  []string             `json:"externalIPs,omitempty"`
	ExternalName string               `json:"externalName,omitempty"`
}

type serviceSummaryStatus struct {
	LoadBalancer corev1.LoadBalancerStatus `json:"loadBalancer,omitempty"`
}

func serviceToSummary(s *corev1.Service) serviceSummary {
	return serviceSummary{
		Kind: "Service", APIVersion: "v1",
		Metadata: projectMeta(s.ObjectMeta, nil),
		Spec: serviceSummarySpec{
			Type:         s.Spec.Type,
			ClusterIP:    s.Spec.ClusterIP,
			Ports:        s.Spec.Ports,
			Selector:     s.Spec.Selector,
			ExternalIPs:  s.Spec.ExternalIPs,
			ExternalName: s.Spec.ExternalName,
		},
		Status: serviceSummaryStatus{LoadBalancer: s.Status.LoadBalancer},
	}
}

// --- ReplicaSet ---

type replicaSetSummary struct {
	Kind       string                  `json:"kind"`
	APIVersion string                  `json:"apiVersion"`
	Metadata   summaryMeta             `json:"metadata"`
	Spec       replicaSetSummarySpec   `json:"spec"`
	Status     replicaSetSummaryStatus `json:"status"`
}

type replicaSetSummarySpec struct {
	Replicas *int32 `json:"replicas,omitempty"`
}

type replicaSetSummaryStatus struct {
	Replicas          int32                        `json:"replicas"`
	ReadyReplicas     int32                        `json:"readyReplicas,omitempty"`
	AvailableReplicas int32                        `json:"availableReplicas,omitempty"`
	Conditions        []appsv1.ReplicaSetCondition `json:"conditions,omitempty"`
}

func replicaSetToSummary(rs *appsv1.ReplicaSet) replicaSetSummary {
	return replicaSetSummary{
		Kind: "ReplicaSet", APIVersion: "apps/v1",
		Metadata: projectMeta(rs.ObjectMeta, nil),
		Spec:     replicaSetSummarySpec{Replicas: rs.Spec.Replicas},
		Status: replicaSetSummaryStatus{
			Replicas:          rs.Status.Replicas,
			ReadyReplicas:     rs.Status.ReadyReplicas,
			AvailableReplicas: rs.Status.AvailableReplicas,
			Conditions:        rs.Status.Conditions,
		},
	}
}

// --- Job ---

type jobSummary struct {
	Kind       string           `json:"kind"`
	APIVersion string           `json:"apiVersion"`
	Metadata   summaryMeta      `json:"metadata"`
	Spec       jobSummarySpec   `json:"spec"`
	Status     jobSummaryStatus `json:"status"`
}

type jobSummarySpec struct {
	Parallelism *int32 `json:"parallelism,omitempty"`
	Completions *int32 `json:"completions,omitempty"`
	Suspend     *bool  `json:"suspend,omitempty"`
}

type jobSummaryStatus struct {
	Active         int32                  `json:"active,omitempty"`
	Succeeded      int32                  `json:"succeeded,omitempty"`
	Failed         int32                  `json:"failed,omitempty"`
	StartTime      *metav1.Time           `json:"startTime,omitempty"`
	CompletionTime *metav1.Time           `json:"completionTime,omitempty"`
	Conditions     []batchv1.JobCondition `json:"conditions,omitempty"`
}

func jobToSummary(j *batchv1.Job) jobSummary {
	return jobSummary{
		Kind: "Job", APIVersion: "batch/v1",
		Metadata: projectMeta(j.ObjectMeta, nil),
		Spec: jobSummarySpec{
			Parallelism: j.Spec.Parallelism,
			Completions: j.Spec.Completions,
			Suspend:     j.Spec.Suspend,
		},
		Status: jobSummaryStatus{
			Active:         j.Status.Active,
			Succeeded:      j.Status.Succeeded,
			Failed:         j.Status.Failed,
			StartTime:      j.Status.StartTime,
			CompletionTime: j.Status.CompletionTime,
			Conditions:     j.Status.Conditions,
		},
	}
}

// --- Event ---

type eventSummary struct {
	Kind           string                 `json:"kind"`
	APIVersion     string                 `json:"apiVersion"`
	Metadata       summaryMeta            `json:"metadata"`
	InvolvedObject corev1.ObjectReference `json:"involvedObject"`
	Reason         string                 `json:"reason,omitempty"`
	Message        string                 `json:"message,omitempty"`
	Source         corev1.EventSource     `json:"source,omitempty"`
	FirstTimestamp metav1.Time            `json:"firstTimestamp,omitempty"`
	LastTimestamp  metav1.Time            `json:"lastTimestamp,omitempty"`
	Count          int32                  `json:"count,omitempty"`
	Type           string                 `json:"type,omitempty"`
}

func eventToSummary(e *corev1.Event) eventSummary {
	return eventSummary{
		Kind: "Event", APIVersion: "v1",
		Metadata:       projectMeta(e.ObjectMeta, nil),
		InvolvedObject: e.InvolvedObject,
		Reason:         e.Reason,
		Message:        e.Message,
		Source:         e.Source,
		FirstTimestamp: e.FirstTimestamp,
		LastTimestamp:  e.LastTimestamp,
		Count:          e.Count,
		Type:           e.Type,
	}
}

// --- Dispatch ---

func typedItemSummary(item any) any {
	switch v := item.(type) {
	case *corev1.Pod:
		return podToSummary(v)
	case *corev1.Service:
		return serviceToSummary(v)
	case *corev1.Event:
		return eventToSummary(v)
	case *appsv1.ReplicaSet:
		return replicaSetToSummary(v)
	case *batchv1.Job:
		return jobToSummary(v)
	default:
		return item
	}
}

// applyTypedSummary projects typed-cache list results into their summary
// shapes. Kinds without a projection (and non-typed results — the dynamic
// path's unstructured slices, which applySummaryStrip owns) pass through
// unchanged, so summary stays best-effort exactly like the dynamic path.
func applyTypedSummary(result any) any {
	switch items := result.(type) {
	case []*corev1.Pod:
		out := make([]any, len(items))
		for i, p := range items {
			out[i] = podToSummary(p)
		}
		return out
	case []*corev1.Service:
		out := make([]any, len(items))
		for i, s := range items {
			out[i] = serviceToSummary(s)
		}
		return out
	case []*corev1.Event:
		out := make([]any, len(items))
		for i, e := range items {
			out[i] = eventToSummary(e)
		}
		return out
	case []*appsv1.ReplicaSet:
		out := make([]any, len(items))
		for i, rs := range items {
			out[i] = replicaSetToSummary(rs)
		}
		return out
	case []*batchv1.Job:
		out := make([]any, len(items))
		for i, j := range items {
			out[i] = jobToSummary(j)
		}
		return out
	case []any:
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = typedItemSummary(item)
		}
		return out
	default:
		return result
	}
}
