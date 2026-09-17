package mcp

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

func TestGetRightsizingRequiresScope(t *testing.T) {
	_, _, err := handleGetRightsizing(context.Background(), nil, getRightsizingInput{})
	if err == nil {
		t.Fatal("scope must be required so a bare call never triggers the cluster scan")
	}
	for _, want := range []string{"workload", "namespace", "cluster", "45s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should teach all three call shapes and the scan's cost, missing %q: %v", want, err)
		}
	}
}

func TestGetRightsizingRejectsUnknownScope(t *testing.T) {
	_, _, err := handleGetRightsizing(context.Background(), nil, getRightsizingInput{Scope: "fleet"})
	if err == nil || !strings.Contains(err.Error(), "fleet") {
		t.Fatalf("expected the rejected scope to be named back, got: %v", err)
	}
}

func TestGetRightsizingClusterScopeRejectsNarrowingParams(t *testing.T) {
	for _, input := range []getRightsizingInput{
		{Scope: "cluster", Namespace: "prod"},
		{Scope: "cluster", Kind: "Deployment"},
		{Scope: "cluster", Name: "checkout"},
	} {
		_, _, err := handleGetRightsizing(context.Background(), nil, input)
		if err == nil {
			t.Errorf("scope=cluster with %+v should be rejected rather than silently ignoring the narrowing", input)
		}
	}
}

func TestGetRightsizingNamespaceScopeRequiresNamespace(t *testing.T) {
	_, _, err := handleGetRightsizing(context.Background(), nil, getRightsizingInput{Scope: "namespace"})
	if err == nil || !strings.Contains(err.Error(), "cluster") {
		t.Fatalf("expected an error routing to scope=cluster for a whole-cluster scan, got: %v", err)
	}
}

func TestGetRightsizingWorkloadScopeRequiresIdentifiers(t *testing.T) {
	for _, input := range []getRightsizingInput{
		{Scope: "workload"},
		{Scope: "workload", Kind: "Deployment"},
		{Scope: "workload", Kind: "Deployment", Namespace: "prod"},
	} {
		_, _, err := handleGetRightsizing(context.Background(), nil, input)
		if err == nil {
			t.Errorf("scope=workload with %+v should demand kind, namespace, and name", input)
		}
	}
}

func rightsizingRow(fit prometheuspkg.RightsizingFit, current, recommended float64) prometheuspkg.RightsizingRow {
	recommendedStr := "recommended"
	row := prometheuspkg.RightsizingRow{
		Container:      "app",
		Resource:       "cpu",
		Fit:            fit,
		Confidence:     prometheuspkg.ConfidenceHigh,
		RecommendedReq: &recommendedStr,
	}
	// A missing request has no current value at all, which is what the ranker
	// keys on — so only populate it when there is one.
	if current > 0 {
		currentStr := "current"
		row.CurrentRequest = &currentStr
		row.CurrentRequestValue = &current
	}
	if recommended > 0 {
		row.RecommendedRequestValue = &recommended
	}
	return row
}

func TestFilterRightsizingRowsDropsBalancedByDefault(t *testing.T) {
	// The unevidenced row belongs to a container with nothing else returned:
	// a returned container keeps its unevidenced rows, which is tested apart.
	unjudged := rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1)
	unjudged.Container = "sidecar"
	rows := []prometheuspkg.RightsizingRow{
		rightsizingRow(prometheuspkg.FitBalanced, 1, 1),
		rightsizingRow(prometheuspkg.FitOversized, 4, 1),
		rightsizingRow(prometheuspkg.FitUnderRequested, 1, 4),
		rightsizingRow(prometheuspkg.FitMissingRequest, 0, 2),
		unjudged,
	}

	filtered := filterRightsizingRows(rows, false, false, 1, false)
	actionable, omitted := filtered.rows, filtered.omitted
	if len(actionable) != 3 {
		t.Fatalf("expected only oversized, under_requested, and missing_request, got %d", len(actionable))
	}
	for _, row := range actionable {
		if row.Fit == prometheuspkg.FitBalanced || row.Fit == prometheuspkg.FitInsufficientHistory {
			t.Errorf("non-actionable fit %q leaked into the default response", row.Fit)
		}
	}

	if omitted.Balanced != 1 || omitted.InsufficientHistory != 1 {
		t.Errorf("omissions must be reported so the model can see rows were withheld, got %+v", omitted)
	}

	includeAll := filterRightsizingRows(rows, true, false, 1, false)
	all, allOmitted := includeAll.rows, includeAll.omitted
	if len(all) != len(rows) {
		t.Errorf("include_balanced should return every row, got %d of %d", len(all), len(rows))
	}
	if allOmitted.total() != 0 {
		t.Errorf("nothing is withheld when include_balanced is set, got %+v", allOmitted)
	}
}

func TestFilterRightsizingRowsFormatsThrottleOnlyWhenMeasured(t *testing.T) {
	ratio := 0.125
	measured := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	measured.ThrottleAvailable = true
	measured.ThrottleRatio = &ratio

	unmeasured := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	unmeasured.ThrottleRatio = &ratio

	out := filterRightsizingRows([]prometheuspkg.RightsizingRow{measured, unmeasured}, false, false, 1, false).rows
	if out[0].ThrottleRatio == nil || *out[0].ThrottleRatio != "12.5%" {
		t.Errorf("measured throttling should be formatted as a percentage, got %v", out[0].ThrottleRatio)
	}
	if out[1].ThrottleRatio != nil {
		t.Error("an unavailable throttle ratio must stay absent — unavailable is not zero")
	}
}

// Ranking must agree with the Rightsizing screen: the workload giving back the
// most CPU or memory once replicas are counted comes first, not the one whose
// request changes by the largest proportion.
func TestScanRankingFollowsReplicaWeightedImpact(t *testing.T) {
	// 200m -> 50m on one replica: a 4x proportional cut, 150m of real CPU.
	small := filterRightsizingRows(
		[]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitOversized, 0.2, 0.05)}, false, false, 1, false)
	// 1500m -> 750m across three replicas: a 2x cut, 2250m of real CPU.
	large := filterRightsizingRows(
		[]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitOversized, 1.5, 0.75)}, false, false, 3, false)

	if prometheuspkg.ImpactScore(large.impact) <= prometheuspkg.ImpactScore(small.impact) {
		t.Errorf("the larger absolute saving must rank first: large=%v small=%v",
			prometheuspkg.ImpactScore(large.impact), prometheuspkg.ImpactScore(small.impact))
	}
	if large.impact.CPU != "-2250m" {
		t.Errorf("impact should carry a formatted replica-weighted quantity, got %q", large.impact.CPU)
	}
	if large.classification != prometheuspkg.ClassReduction {
		t.Errorf("an oversized workload with no review flags is a reduction, got %q", large.classification)
	}
}

// Class decides the order before impact does, and the screen leads with
// reductions — the tool is answering "where is the waste", so a reduction sorts
// above an increase even when the increase is proportionally larger.
func TestScanRankingOrdersByClassBeforeImpact(t *testing.T) {
	increase := filterRightsizingRows(
		[]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitUnderRequested, 0.05, 4)}, false, false, 10, false)
	reduction := filterRightsizingRows(
		[]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitOversized, 4, 3.9)}, false, false, 1, false)

	if prometheuspkg.ImpactScore(increase.impact) <= prometheuspkg.ImpactScore(reduction.impact) {
		t.Fatal("test setup: the increase should carry the larger raw impact")
	}
	if !prometheuspkg.RightsizingRankLess(
		reduction.classification, reduction.impact, "a",
		increase.classification, increase.impact, "b") {
		t.Error("a reduction must sort above an increase — class outranks impact, matching the Rightsizing screen")
	}
}

// A workload with both an under-requested and an oversized container is
// classified as an increase: the under-request is the one that takes it down.
func TestClassificationPrefersIncreaseOverReduction(t *testing.T) {
	under := rightsizingRow(prometheuspkg.FitUnderRequested, 0.05, 0.2)
	over := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	over.Container = "sidecar"

	filtered := filterRightsizingRows([]prometheuspkg.RightsizingRow{over, under}, false, false, 1, false)
	if filtered.classification != prometheuspkg.ClassIncrease {
		t.Errorf("an under-requested container decides the workload's class, got %q", filtered.classification)
	}
}

// The OOM shape — request fine, limit too low — classifies as balanced because
// fit is settled from the request alone. Filtering on fit therefore dropped the
// one row an agent asking "why does this OOM" needs.
func TestFilterKeepsOOMEvidenceDespiteBalancedFit(t *testing.T) {
	row := rightsizingRow(prometheuspkg.FitBalanced, 1, 1)
	row.Resource = "memory"
	row.CurrentPodOOM = true

	filtered := filterRightsizingRows([]prometheuspkg.RightsizingRow{row}, false, false, 1, false)
	if len(filtered.rows) != 1 {
		t.Fatalf("a row carrying OOM evidence must never be omitted as balanced, got %d rows", len(filtered.rows))
	}
	if filtered.omitted.Balanced != 0 {
		t.Errorf("the OOM row was counted as omitted-balanced: %+v", filtered.omitted)
	}
	if filtered.classification != prometheuspkg.ClassReview {
		t.Errorf("OOM history demands manual review, got %q", filtered.classification)
	}
}

// oomEvidenceAvailable is meaningless on a CPU row; a literal false there reads
// as an evidence gap the agent should weigh.
func TestOOMAvailabilityOnlyEmittedForMemoryRows(t *testing.T) {
	cpu := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	mem := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	mem.Resource = "memory"

	out := filterRightsizingRows([]prometheuspkg.RightsizingRow{cpu, mem}, false, false, 1, false).rows
	if out[0].OOMEvidenceAvailable != nil {
		t.Error("a CPU row must not carry oomEvidenceAvailable")
	}
	if out[1].OOMEvidenceAvailable == nil {
		t.Error("a memory row must report whether OOM evidence was available")
	}
}

func TestRightsizingGuidanceWarnsOnPartialScans(t *testing.T) {
	complete := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanComplete, scope: "cluster",
	})
	if strings.Contains(complete, "partial") {
		t.Errorf("a complete scan should not be described as partial: %q", complete)
	}
	if !strings.Contains(complete, "confidence") || !strings.Contains(complete, "7 days") {
		t.Errorf("guidance must state the 7-day window and the confidence caveat: %q", complete)
	}
	if !strings.Contains(complete, "include_balanced") {
		t.Errorf("guidance should say correctly-sized rows were omitted: %q", complete)
	}

	partial := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, includeBalanced: true, scope: "cluster",
		coverage: &prometheuspkg.RightsizingScanCoverage{RestrictedKinds: []string{"DaemonSet"}},
	})
	if !strings.Contains(partial, "cluster-wide") {
		t.Errorf("a partial scan must warn against cluster-wide conclusions: %q", partial)
	}
	if strings.Contains(partial, "include_balanced") {
		t.Errorf("include_balanced=true should not carry the omission note: %q", partial)
	}
}

// A fixed paragraph naming restrictedKinds and completedBatches was wrong
// whenever those were empty and the cause was row-level or scope-level, which
// is the common case. The guidance has to name the causes actually present.
func TestPartialGuidanceNamesTheCausePresent(t *testing.T) {
	rowLevel := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster",
		reason:   reasonRowEvidenceIncomplete,
		coverage: &prometheuspkg.RightsizingScanCoverage{Batches: 1, CompletedBatches: 1},
		omitted:  rightsizingOmissions{InsufficientHistory: 3},
	})
	if !strings.Contains(rowLevel, "insufficientHistory") {
		t.Errorf("the real cause (3 rows short of history) must be named: %q", rowLevel)
	}
	if strings.Contains(rowLevel, "restrictedKinds") || strings.Contains(rowLevel, "stopped after") {
		t.Errorf("guidance must not point at causes this response does not carry: %q", rowLevel)
	}

	cached := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster",
		coverage: &prometheuspkg.RightsizingScanCoverage{PartiallyCachedKinds: []string{"Deployment"}},
	})
	if !strings.Contains(cached, "partiallyCachedKinds") {
		t.Errorf("partial caching can be the only reason for partial, so it must be named: %q", cached)
	}

	// Only the deadline warning makes a short batch count an early stop.
	truncatedScan := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster",
		coverage: &prometheuspkg.RightsizingScanCoverage{
			Batches: 3, CompletedBatches: 1,
			WorkloadsDiscovered: 10, WorkloadsEvaluated: 4,
		},
		deadlineExceeded: true,
	})
	if !strings.Contains(truncatedScan, "stopped early, evaluating 4 of 10 workloads") {
		t.Errorf("an early stop must say how many workloads went unevaluated: %q", truncatedScan)
	}
	if strings.Contains(truncatedScan, "had a failure") || strings.Contains(truncatedScan, "failed query") {
		t.Errorf("batches the deadline left unrun are not failed batches: %q", truncatedScan)
	}
	stoppedAfterFailure := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster",
		coverage:         &prometheuspkg.RightsizingScanCoverage{Batches: 3, CompletedBatches: 0, WorkloadsDiscovered: 10, WorkloadsEvaluated: 4},
		deadlineExceeded: true, batchQueryFailed: true,
	})
	if !strings.Contains(stoppedAfterFailure, "stopped early") || !strings.Contains(stoppedAfterFailure, "failed query") {
		t.Errorf("a deadline must not hide a failure in a batch that ran: %q", stoppedAfterFailure)
	}

	// Dropped Deployments leave evaluated below discovered with every batch run.
	droppedDeployments := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster",
		coverage: &prometheuspkg.RightsizingScanCoverage{
			Batches: 3, CompletedBatches: 2,
			WorkloadsDiscovered: 10, WorkloadsEvaluated: 6,
			UnavailableKinds: []string{"Deployment"},
		},
	})
	if strings.Contains(droppedDeployments, "stopped") {
		t.Errorf("a scan that ran every batch did not stop: %q", droppedDeployments)
	}

	// CompletedBatches counts batches whose queries all answered, not batches
	// that ran. With every workload evaluated the scan did not stop, and
	// saying so would send the agent to narrow a scope that was never cut.
	failedQueries := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster",
		coverage: &prometheuspkg.RightsizingScanCoverage{
			Batches: 3, CompletedBatches: 1,
			WorkloadsDiscovered: 10, WorkloadsEvaluated: 10,
		},
	})
	if strings.Contains(failedQueries, "stopped after") {
		t.Errorf("a fully-evaluated scan did not stop early: %q", failedQueries)
	}
	if !strings.Contains(failedQueries, "2 of 3 query batches had a failure") {
		t.Errorf("failed query batches must still be named as the cause: %q", failedQueries)
	}
}

// An unavailable response has no rows and no omitted counts, so guidance about
// what was omitted describes a response the caller did not receive.
func TestUnavailableGuidanceDropsInapplicableText(t *testing.T) {
	got := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanUnavailable, scope: "cluster",
		reason: "owner_metrics_missing",
	})
	if strings.Contains(got, "omitted counts") || strings.Contains(got, "include_balanced") {
		t.Errorf("unavailable responses must not describe omissions: %q", got)
	}
	if !strings.Contains(got, "remediation") {
		t.Errorf("unavailable guidance should point at reason and remediation: %q", got)
	}
}

// reductionLimited means the recommendation is a clamped step, not the fitted
// value — without that said, a 5m observation recommending 750m looks wrong.
func TestGuidanceExplainsClampedReductions(t *testing.T) {
	got := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanComplete, scope: "cluster", reductionLimited: true,
	})
	if !strings.Contains(got, "bounded step") || !strings.Contains(got, "three quarters") {
		t.Errorf("a clamped reduction needs explaining in the response: %q", got)
	}
}

func TestRightsizingUnavailableCarriesRemediation(t *testing.T) {
	resp := rightsizingUnavailable("cluster", "prometheus_unavailable")
	if resp.State != prometheuspkg.RightsizingScanUnavailable {
		t.Errorf("unexpected state %q", resp.State)
	}
	if !strings.Contains(resp.Remediation, "kube-state-metrics") {
		t.Errorf("remediation should name the history source recommendations need, got: %q", resp.Remediation)
	}
	if resp.Workloads == nil {
		t.Error("workloads should serialize as an empty array, not null")
	}
}

func TestRightsizingScanKindsComeFromTheSharedCatalogue(t *testing.T) {
	// Both surfaces range over prometheuspkg.RightsizingScanKinds, which is what
	// makes drift between REST and MCP impossible. This pins the catalogue's
	// contents; a test comparing against its own literal would not.
	want := map[string]string{"Deployment": "deployments", "StatefulSet": "statefulsets", "DaemonSet": "daemonsets"}
	if len(prometheuspkg.RightsizingScanKinds) != len(want) {
		t.Fatalf("scan catalogue changed: %+v", prometheuspkg.RightsizingScanKinds)
	}
	for _, kind := range prometheuspkg.RightsizingScanKinds {
		if want[kind.Kind] != kind.Resource {
			t.Errorf("kind %q maps to resource %q, expected %q", kind.Kind, kind.Resource, want[kind.Kind])
		}
	}
}

func TestRightsizingRemediationCoversTheScanDeadline(t *testing.T) {
	// The scan budget covers authorization as well as the queries, so a blown
	// deadline is reachable on the authorization path too.
	if rightsizingRemediation("scan_deadline_exceeded") == "" {
		t.Error("a blown scan budget reaches the model with no remediation")
	}
}

func TestRightsizingRemediationCoversScanFailureReasons(t *testing.T) {
	// The scan reports its own vocabulary; an unmapped reason leaves the model
	// with a bare code and nothing to tell the user to do about it.
	for _, reason := range []string{
		"prometheus_unavailable",
		"owner_metrics_query_failed",
		"owner_metrics_missing",
		"deployment_owner_metrics_missing",
		"resource_cache_unavailable",
		"workload_kinds_unavailable",
		"access_denied",
	} {
		if rightsizingRemediation(reason) == "" {
			t.Errorf("reason %q reaches the model with no remediation", reason)
		}
	}
	if rightsizingRemediation("something_new") != "" {
		t.Error("unknown reasons should get no invented remediation")
	}
}

func TestGetRightsizingRejectsUnsupportedKindBeforeAuthorizing(t *testing.T) {
	// A SAR against apps/pods denies on a resource that cannot exist, which
	// would answer "forbidden" to what is really an unsupported-kind mistake.
	_, _, err := handleGetRightsizing(context.Background(), nil, getRightsizingInput{
		Scope: "workload", Kind: "Pod", Namespace: "cost-demo", Name: "x",
	})
	if err == nil {
		t.Fatal("expected Pod to be rejected")
	}
	if strings.Contains(err.Error(), "forbidden") {
		t.Errorf("unsupported kind must not surface as an authorization failure: %v", err)
	}
	if !strings.Contains(err.Error(), "container template") {
		t.Errorf("error should explain why Pods are the wrong granularity: %v", err)
	}
}

func TestRightsizingPartialGuidanceNamesEveryCoverageCause(t *testing.T) {
	// Every coverage cause must be reachable from the guidance when it is the
	// one present; naming a cause that is absent is the opposite failure and is
	// covered by TestPartialGuidanceNamesTheCausePresent.
	all := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster",
		coverage: &prometheuspkg.RightsizingScanCoverage{
			RestrictedKinds:      []string{"DaemonSet"},
			UnavailableKinds:     []string{"Deployment"},
			PartiallyCachedKinds: []string{"StatefulSet"},
			Batches:              3,
			CompletedBatches:     1,
			WorkloadsDiscovered:  10,
			WorkloadsEvaluated:   4,
		},
		deadlineExceeded: true,
	})
	for _, cause := range []string{"restrictedKinds", "unavailableKinds", "partiallyCachedKinds", "4 of 10 workloads"} {
		if !strings.Contains(all, cause) {
			t.Errorf("partial guidance omits %q, so that cause reads as complete coverage: %q", cause, all)
		}
	}
}

func TestNarrowedClusterScanGuidanceRefusesClusterWideFraming(t *testing.T) {
	// scope="cluster" resolves to what the identity can list, or to the
	// --namespace pin. A complete-looking scan over two namespaces must not
	// read as a cluster-wide answer.
	narrowed := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster",
		namespaceScope: []string{"prod", "staging"},
	})
	if !strings.Contains(narrowed, "namespaceScope") {
		t.Errorf("guidance must point at the field naming the real scope: %q", narrowed)
	}
	if !strings.Contains(narrowed, "2 namespace") {
		t.Errorf("guidance must state how many namespaces were reached: %q", narrowed)
	}

	full := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanComplete, scope: "cluster",
	})
	if strings.Contains(full, "namespaceScope") {
		t.Errorf("an unnarrowed scan should carry no scope caveat: %q", full)
	}
}

func TestNarrowedClusterScopeRemediationNamesTheRealCause(t *testing.T) {
	// Unpinned, the only cause left is RBAC — attributing it to --namespace
	// would send the reader to a flag that was never passed.
	remediation := rightsizingRemediation(reasonNamespaceScopeLimited)
	if !strings.Contains(remediation, "cluster-wide") {
		t.Errorf("remediation must state what the scan could not reach: %q", remediation)
	}
	if strings.Contains(remediation, "--namespace") {
		t.Errorf("an unpinned radar must not blame the pin: %q", remediation)
	}

	// The pin is a startup flag, not a permissions problem.
	pinned := rightsizingRemediation(ReasonOutsideNamespaceScope)
	if !strings.Contains(pinned, "--namespace") || !strings.Contains(pinned, "not a permissions problem") {
		t.Errorf("a pin-excluded scope must be attributed to the flag: %q", pinned)
	}
}

func TestOmissionCountersAreExclusive(t *testing.T) {
	// A query-error row already carries fit=insufficient_history; counting it in
	// both buckets would overstate how much history is missing.
	queryErr := rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1)
	queryErr.QueryError = "upstream 500"

	omitted := filterRightsizingRows([]prometheuspkg.RightsizingRow{
		queryErr,
		rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1),
		rightsizingRow(prometheuspkg.FitBalanced, 1, 1),
	}, false, false, 1, false).omitted

	if omitted.QueryError != 1 || omitted.InsufficientHistory != 1 || omitted.Balanced != 1 {
		t.Errorf("categories must be exclusive, got %+v", omitted)
	}
	if omitted.total() != 3 {
		t.Errorf("every withheld row should be counted once, got %d", omitted.total())
	}
}

func TestIncompleteEvidenceReadsRawRowsNotSurvivors(t *testing.T) {
	// Derived from the raw rows so include_balanced=true, which withholds
	// nothing, still reports partial when a query failed.
	queryErr := rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1)
	queryErr.QueryError = "upstream 500"
	rows := []prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitOversized, 4, 1), queryErr}

	if !filterRightsizingRows(rows, true, false, 1, false).incompleteEvidence {
		t.Error("a query error is incomplete evidence regardless of what survives filtering")
	}
	if filterRightsizingRows([]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitOversized, 4, 1)}, false, false, 1, false).incompleteEvidence {
		t.Error("fully evidenced rows must not be reported as partial")
	}
}

func TestBlockedRecommendationsRankBelowRealOnes(t *testing.T) {
	// A row with a request but no recommendation had its evidence blocked (HPA,
	// OOM, query error). It contributes no impact, so it must not consume the
	// limit ahead of a workload with something to act on.
	blocked := rightsizingRow(prometheuspkg.FitOversized, 4, 0)
	blocked.RecommendedReq = nil
	blockedRows := filterRightsizingRows([]prometheuspkg.RightsizingRow{blocked}, false, false, 1, false)
	realRows := filterRightsizingRows([]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitOversized, 4, 1)}, false, false, 1, false)

	if prometheuspkg.ImpactScore(blockedRows.impact) != 0 {
		t.Errorf("a blocked recommendation has no impact to rank on, got %v", prometheuspkg.ImpactScore(blockedRows.impact))
	}
	if !prometheuspkg.RightsizingRankLess(
		realRows.classification, realRows.impact, "a",
		blockedRows.classification, blockedRows.impact, "b") {
		t.Error("a real recommendation must rank above a blocked one")
	}
}

func TestEvidenceAvailabilityFlagsSurvive(t *testing.T) {
	// hpaManaged=false with no HPA evidence means unknown, not "not autoscaled",
	// and a request cut is unsafe in both the OOM and HPA unknown cases.
	unknown := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	unknown.Resource = "memory"
	known := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	known.Resource = "memory"
	known.HPAEvidenceAvailable = true
	known.OOMEvidenceAvailable = true

	out := filterRightsizingRows([]prometheuspkg.RightsizingRow{unknown, known}, false, false, 1, false).rows
	if out[0].HPAEvidenceAvailable || (out[0].OOMEvidenceAvailable != nil && *out[0].OOMEvidenceAvailable) {
		t.Error("missing evidence must not be reported as available")
	}
	if !out[1].HPAEvidenceAvailable || out[1].OOMEvidenceAvailable == nil || !*out[1].OOMEvidenceAvailable {
		t.Error("available evidence must be reported so the model can trust hpaManaged/OOM flags")
	}
}

func TestGetRightsizingNamespaceScopeRejectsWorkloadIdentifiers(t *testing.T) {
	for _, input := range []getRightsizingInput{
		{Scope: "namespace", Namespace: "prod", Kind: "Deployment"},
		{Scope: "namespace", Namespace: "prod", Name: "checkout"},
	} {
		_, _, err := handleGetRightsizing(context.Background(), nil, input)
		if err == nil {
			t.Errorf("%+v should be rejected rather than silently running a whole-namespace scan", input)
		} else if !strings.Contains(err.Error(), `scope="workload"`) {
			t.Errorf("error should route to the cheaper workload scope, got: %v", err)
		}
	}
}

func TestWorkloadScopeGuidanceDoesNotCiteScanCoverage(t *testing.T) {
	// A workload response carries no coverage object, so pointing the model at
	// coverage.completedBatches sends it to a field that is not there.
	workload := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "workload",
	})
	for _, absent := range []string{"completedBatches", "restrictedKinds", "unavailableKinds"} {
		if strings.Contains(workload, absent) {
			t.Errorf("workload guidance cites %q, which only exists on scan scopes: %q", absent, workload)
		}
	}
	if !strings.Contains(workload, "omitted") {
		t.Errorf("workload guidance should point at the omitted counts: %q", workload)
	}
}

func TestBlockedRowsClassifyAsUnactionable(t *testing.T) {
	// A missing request with no recommendation has nothing to act on, so it
	// must not outrank a workload that does.
	blocked := rightsizingRow(prometheuspkg.FitMissingRequest, 0, 0)
	blocked.RecommendedReq = nil
	blockedRows := filterRightsizingRows([]prometheuspkg.RightsizingRow{blocked}, false, false, 1, false)
	if blockedRows.classification == prometheuspkg.ClassIncrease {
		t.Error("a missing request with no recommendation is not an actionable increase")
	}

	actionable := filterRightsizingRows(
		[]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitMissingRequest, 0, 2)}, false, false, 1, false)
	if actionable.classification != prometheuspkg.ClassIncrease {
		t.Errorf("a missing request WITH a recommendation is an increase, got %q", actionable.classification)
	}
	if !prometheuspkg.RightsizingRankLess(
		actionable.classification, actionable.impact, "a",
		blockedRows.classification, blockedRows.impact, "b") {
		t.Error("an actionable increase must rank above a blocked row")
	}
}

func TestExplicitZeroRequestStillCountsItsFullImpact(t *testing.T) {
	// An explicit 0 request has a non-nil CurrentRequest, so impact has to read
	// the value rather than the pointer or the increase would compute as zero.
	zero := rightsizingRow(prometheuspkg.FitMissingRequest, 0, 2)
	explicit := "0"
	zero.CurrentRequest = &explicit
	zeroValue := 0.0
	zero.CurrentRequestValue = &zeroValue

	filtered := filterRightsizingRows([]prometheuspkg.RightsizingRow{zero}, false, false, 2, false)
	if filtered.classification != prometheuspkg.ClassIncrease {
		t.Errorf("an explicit zero request is still a missing request, got %q", filtered.classification)
	}
	// 2 cores recommended from nothing, across 2 replicas.
	if filtered.impact.CPU != "+4000m" {
		t.Errorf("impact must be measured from zero, not skipped, got %q", filtered.impact.CPU)
	}
}

func TestSuppressedRecommendationsCountAsIncompleteEvidence(t *testing.T) {
	// Radar sets these reasons only when the missing evidence actually blocked a
	// recommendation, so reporting state=complete would present a withheld
	// verdict as a clean one — and a memory cut on an unknown-OOM container is
	// exactly the unsafe change this guards.
	for _, reason := range []string{
		prometheuspkg.ReasonHPAEvidenceUnavailable,
		prometheuspkg.ReasonOOMEvidenceUnavailable,
	} {
		row := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
		row.RecommendationReason = reason
		if !filterRightsizingRows([]prometheuspkg.RightsizingRow{row}, true, false, 1, false).incompleteEvidence {
			t.Errorf("reason %q suppressed the recommendation and must not report as complete", reason)
		}
	}

	// A recommendation that stands on full evidence stays complete.
	fine := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	fine.RecommendationReason = "request_within_fit_range"
	if filterRightsizingRows([]prometheuspkg.RightsizingRow{fine}, true, false, 1, false).incompleteEvidence {
		t.Error("a fully evidenced row must not downgrade the response to partial")
	}
}

// docsPath is the operator-facing contract for the rightsizing response.
const docsPath = "../../docs/mcp.md"

func TestEveryRecommendationReasonIsDocumented(t *testing.T) {
	// A bare reason string reaching an agent produces "no recommendation
	// available", which loses the safety reasoning that withheld it. The UI has
	// a sentence for each in getRightsizingExplanation; the MCP surface points
	// at docs/mcp.md instead, so every value has to appear there.
	raw, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("read %s: %v", docsPath, err)
	}
	docs := string(raw)

	for _, reason := range []string{
		"insufficient_history",
		"request_within_fit_range",
		"hpa_managed",
		"hpa_evidence_unavailable",
		"oom_evidence",
		"oom_evidence_unavailable",
		"recommended_request_exceeds_limit",
	} {
		if !strings.Contains(docs, reason) {
			t.Errorf("recommendationReason %q reaches agents with nothing in %s explaining it", reason, docsPath)
		}
	}
}

// Remediation is the field the tool description points agents at, and it is set
// from reason on every state. A reason the engine can produce but this layer
// does not recognize reaches the agent as a bare string with nothing to act on.
func TestEveryScanReasonCarriesRemediation(t *testing.T) {
	// Reasons the scan engine and this layer can assign, gathered from the
	// assignment sites rather than from a hand-kept list.
	engineReasons := scanReasonsAssignedInEngine(t)
	engineReasons = append(engineReasons,
		reasonRowEvidenceIncomplete,
		reasonNamespaceScopeLimited,
		reasonNamespacesExcluded,
		ReasonOutsideNamespaceScope,
		"access_denied",
		"scan_deadline_exceeded",
		"prometheus_unavailable",
	)

	for _, reason := range engineReasons {
		if rightsizingRemediation(reason) == "" {
			t.Errorf("reason %q reaches the agent with no remediation", reason)
		}
	}
}

// scanReasonsAssignedInEngine reads the machine reasons out of the scan engine
// so a reason added there without a remediation here fails CI, rather than
// silently shipping a response an agent cannot act on.
func scanReasonsAssignedInEngine(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../prometheus/rightsizing_scan.go")
	if err != nil {
		t.Fatalf("reading the scan engine: %v", err)
	}
	pattern := regexp.MustCompile(`(?:resp\.Reason|\.Reason) = "([a-z][a-z0-9_]*)"`)
	seen := map[string]bool{}
	var reasons []string
	for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			reasons = append(reasons, match[1])
		}
	}
	if len(reasons) == 0 {
		t.Fatal("found no machine reasons in the scan engine — the pattern no longer matches")
	}
	return reasons
}

// The Rightsizing screen ranks each container as its own entry, so a workload
// whose sidecar has no evidence still shows its oversized app container at the
// top. Classifying every row of the workload at once demoted the whole workload
// to need_data and pushed real savings past the response limit.
func TestUnevidencedSidecarDoesNotDemoteTheWorkload(t *testing.T) {
	app := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	sidecar := rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1)
	sidecar.Container = "istio-proxy"

	filtered := filterRightsizingRows([]prometheuspkg.RightsizingRow{app, sidecar}, false, false, 3, false)
	if filtered.classification != prometheuspkg.ClassReduction {
		t.Fatalf("workload must rank by its best container, got %q", filtered.classification)
	}
	// The sidecar is still reported as missing evidence, not as a clean bill.
	if !filtered.incompleteEvidence || filtered.omitted.InsufficientHistory != 1 {
		t.Errorf("the unevidenced sidecar must still be reported, got incomplete=%v omitted=%+v",
			filtered.incompleteEvidence, filtered.omitted)
	}
}

// A workload with nothing actionable anywhere still classifies as need_data:
// taking the best container must not upgrade a workload that has no evidence.
func TestEveryContainerUnevidencedStaysNeedData(t *testing.T) {
	first := rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1)
	second := rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1)
	second.Container = "istio-proxy"

	filtered := filterRightsizingRows([]prometheuspkg.RightsizingRow{first, second}, false, false, 1, false)
	if filtered.classification != prometheuspkg.ClassNeedData {
		t.Fatalf("expected need_data when no container has evidence, got %q", filtered.classification)
	}
}

// Dropping unevidenced containers must not let the screen's reduction-first
// sort order overturn the safety precedence: a workload with both an
// under-requested container and an oversized one is still an increase, even
// when a third container has no evidence at all.
func TestUnevidencedContainerDoesNotFlipIncreaseToReduction(t *testing.T) {
	under := rightsizingRow(prometheuspkg.FitUnderRequested, 0.05, 0.2)
	over := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	over.Container = "sidecar"
	blind := rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1)
	blind.Container = "istio-proxy"

	filtered := filterRightsizingRows([]prometheuspkg.RightsizingRow{over, under, blind}, false, false, 1, false)
	if filtered.classification != prometheuspkg.ClassIncrease {
		t.Errorf("under-requested must still decide the class, got %q", filtered.classification)
	}
}

// classifyRightsizingFit settles fit from the request alone, so a container
// throttled against a too-low limit is reported balanced. The guidance promises
// throttled rows are always returned regardless of fit; keeping that promise is
// what surfaces the row an agent is actually looking for.
func TestFilterRightsizingRowsKeepsThrottledBalancedRows(t *testing.T) {
	ratio := 0.25
	throttled := rightsizingRow(prometheuspkg.FitBalanced, 1, 1)
	throttled.ThrottleAvailable = true
	throttled.ThrottleRatio = &ratio

	quiet := rightsizingRow(prometheuspkg.FitBalanced, 1, 1)

	filtered := filterRightsizingRows([]prometheuspkg.RightsizingRow{throttled, quiet}, false, false, 1, false)
	if len(filtered.rows) != 1 {
		t.Fatalf("the throttled row must survive the default filter, got %d rows", len(filtered.rows))
	}
	if filtered.rows[0].ThrottleRatio == nil || *filtered.rows[0].ThrottleRatio != "25.0%" {
		t.Errorf("the surviving row should carry its throttle ratio, got %v", filtered.rows[0].ThrottleRatio)
	}
	if filtered.omitted.Balanced != 1 {
		t.Errorf("only the unthrottled balanced row is omitted, got %d", filtered.omitted.Balanced)
	}
}

// Throttling below the review threshold is ordinary; keeping those rows would
// return every balanced container and bury the ones worth reading.
func TestFilterRightsizingRowsDropsLightlyThrottledBalancedRows(t *testing.T) {
	ratio := 0.02
	light := rightsizingRow(prometheuspkg.FitBalanced, 1, 1)
	light.ThrottleAvailable = true
	light.ThrottleRatio = &ratio

	filtered := filterRightsizingRows([]prometheuspkg.RightsizingRow{light}, false, false, 1, false)
	if len(filtered.rows) != 0 {
		t.Errorf("light throttling is not a reason to keep a balanced row: %+v", filtered.rows)
	}
}

// The filter's contract says a throttled row is never dropped. fit is settled
// from the request alone, so a container throttled against a too-low limit
// reads as balanced — and NeedsManualReview only covers throttling on a row
// that also carries a reduction, so the balanced one fell through.
func TestFilterKeepsThrottledRowDespiteBalancedFit(t *testing.T) {
	row := rightsizingRow(prometheuspkg.FitBalanced, 1, 1)
	ratio := 0.35
	row.ThrottleAvailable = true
	row.ThrottleRatio = &ratio

	filtered := filterRightsizingRows([]prometheuspkg.RightsizingRow{row}, false, false, 1, false)
	if len(filtered.rows) != 1 {
		t.Fatalf("a throttled row must survive the default filter, got %d rows", len(filtered.rows))
	}
	if filtered.omitted.total() != 0 {
		t.Errorf("the throttled row must not be counted as withheld, got %+v", filtered.omitted)
	}
}

// A workload the response has already declared unjudgeable must not carry a
// classification that reads as a verdict that it is correctly sized.
func TestWorkloadWithNoRowsIsNeedDataNotInRange(t *testing.T) {
	filtered := filterRightsizingRows(nil, false, false, 1, false)
	if filtered.classification != prometheuspkg.ClassNeedData {
		t.Errorf("no rows means no evidence, got %q", filtered.classification)
	}
}

func TestWorkloadScopeHonoursTheNamespaceAllowList(t *testing.T) {
	// An exact "get" grant outside the caller's namespace allow-list must not
	// reach the SA-backed cache — the REST route denies the same request.
	ctx := withTestUserPerms(t, "bob", nil, []string{"team-a"})
	getPermCache().Get("bob", nil).SetCanI("get", "apps", "deployments", "team-b", true)

	_, _, err := handleGetRightsizing(ctx, nil, getRightsizingInput{
		Scope: "workload", Kind: "Deployment", Namespace: "team-b", Name: "api",
	})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected forbidden outside the allow-list, got %v", err)
	}
}

// A scaled-to-zero workload classifies as review, which the Rightsizing screen
// lists among its actions; filtering its balanced rows dropped the workload.
func TestFilterKeepsScaledToZeroWorkloadRows(t *testing.T) {
	row := rightsizingRow(prometheuspkg.FitBalanced, 1, 1)
	filtered := filterRightsizingRows([]prometheuspkg.RightsizingRow{row}, false, false, 0, true)
	if filtered.classification != prometheuspkg.ClassReview {
		t.Fatalf("classification = %q, want review", filtered.classification)
	}
	if len(filtered.rows) != 1 || filtered.omitted.total() != 0 {
		t.Errorf("a scaled-to-zero workload must keep its rows, got %d rows, omitted %+v", len(filtered.rows), filtered.omitted)
	}
}

func TestThrottleAvailabilityOnlyEmittedForCPURows(t *testing.T) {
	cpu := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	memory := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	memory.Resource = "memory"

	out := filterRightsizingRows([]prometheuspkg.RightsizingRow{cpu, memory}, false, false, 1, false).rows
	if out[0].ThrottleAvailable == nil || *out[0].ThrottleAvailable {
		t.Errorf("an unmeasured CPU row must say throttleAvailable=false, got %v", out[0].ThrottleAvailable)
	}
	if out[1].ThrottleAvailable != nil {
		t.Error("throttling never gates a memory recommendation; the flag must be absent there")
	}
}

// The engine computes a demand target before its HPA, OOM and limit checks
// return, so it exists on rows whose recommendation was withheld. Emitting it
// there would hand the agent a value for the null those checks left.
func TestDemandTargetOnlyAccompaniesAClampedRecommendation(t *testing.T) {
	calculated := "253Mi"
	clamped := rightsizingRow(prometheuspkg.FitOversized, 4, 2)
	clamped.Resource = "memory"
	clamped.ReductionLimited = true
	clamped.CalculatedReq = &calculated
	clamped.Observed = &prometheuspkg.ObservedStatistic{Name: "Max", Value: 220 * 1024 * 1024, Formatted: "220Mi"}

	out := filterRightsizingRows([]prometheuspkg.RightsizingRow{clamped}, false, true, 1, false).rows
	if out[0].DemandTarget == nil || out[0].DemandTarget.Value != "253Mi" || out[0].DemandTarget.Basis == "" {
		t.Fatalf("a clamped row must carry its demand target and basis, got %+v", out[0].DemandTarget)
	}
	if out[0].ObservedStatistic != "Max" {
		t.Errorf("observedStatistic = %q, want Max", out[0].ObservedStatistic)
	}

	for _, reason := range []string{"hpa_managed", "hpa_evidence_unavailable", "oom_evidence", "oom_evidence_unavailable", "recommended_request_exceeds_limit", "request_within_fit_range"} {
		withheld := clamped
		withheld.RecommendedReq = nil
		withheld.RecommendedRequestValue = nil
		withheld.ReductionLimited = false
		withheld.RecommendationReason = reason
		row := filterRightsizingRows([]prometheuspkg.RightsizingRow{withheld}, false, true, 1, false).rows[0]
		if row.DemandTarget != nil {
			t.Errorf("%s: a withheld recommendation must not expose the demand target, got %+v", reason, row.DemandTarget)
		}
	}
}

func TestGuidanceWarnsAgainstApplyingTheDemandTarget(t *testing.T) {
	got := rightsizingGuidance(rightsizingGuidanceInput{state: prometheuspkg.RightsizingScanComplete, scope: "cluster", reductionLimited: true})
	for _, want := range []string{"demandTarget is the demand-based end state, not a value to apply", "risks OOM"} {
		if !strings.Contains(got, want) {
			t.Errorf("clamped guidance missing %q: %q", want, got)
		}
	}
}

// One failed query makes a container need_data; without the failed row the
// workload's class has no visible cause beside a clean recommendation.
func TestReturnedContainerKeepsItsFailedRow(t *testing.T) {
	failed := rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 0)
	failed.RecommendedReq = nil
	failed.QueryError = prometheuspkg.RowUsageQueryFailed
	memory := rightsizingRow(prometheuspkg.FitOversized, 4, 2)
	memory.Resource = "memory"
	sidecarFailed := failed
	sidecarFailed.Container = "sidecar"

	filtered := filterRightsizingRows([]prometheuspkg.RightsizingRow{failed, memory, sidecarFailed}, false, false, 2, false)
	if len(filtered.rows) != 2 || filtered.rows[0].QueryError == "" {
		t.Fatalf("the returned container must keep its failed row, got %+v", filtered.rows)
	}
	if filtered.returnedQueryErrors != 1 || filtered.omitted.QueryError != 1 {
		t.Errorf("returned=%d omitted=%d, want 1 and 1: a container with nothing else returned stays omitted", filtered.returnedQueryErrors, filtered.omitted.QueryError)
	}
	if filtered.classification != prometheuspkg.ClassNeedData {
		t.Errorf("classification = %q, want need_data: the missing evidence still decides the rank", filtered.classification)
	}

	pointQueryErrorsAtWarnings(filtered.rows)
	if got := filtered.rows[0].QueryError; got != "cpu usage query failed; see warnings code cpu_query_failed" {
		t.Errorf("queryError = %q", got)
	}

	guidance := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster", reason: "some_evidence_unavailable",
		coverage: &prometheuspkg.RightsizingScanCoverage{}, omitted: filtered.omitted, returnedQueryErrors: filtered.returnedQueryErrors,
	})
	for _, want := range []string{"2 row(s) failed their usage query (1 in omitted.queryError, 1 returned with queryError)", "not because that recommendation is doubtful"} {
		if !strings.Contains(guidance, want) {
			t.Errorf("guidance missing %q: %q", want, guidance)
		}
	}
}

func TestRightsizingScanNamespacesValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		scope string
		input getRightsizingInput
		want  string
	}{
		// A comma-joined name authorizes as a namespace that does not exist and
		// used to come back as a complete scan with no workloads.
		"comma in namespace":      {"namespace", getRightsizingInput{Namespace: "dev,staging"}, `namespaces: ["dev", "staging"]`},
		"both forms":              {"namespace", getRightsizingInput{Namespace: "dev", Namespaces: []string{"staging"}}, "not both"},
		"empty entry":             {"namespace", getRightsizingInput{Namespaces: []string{"dev", " "}}, "non-empty"},
		"cluster with namespaces": {"cluster", getRightsizingInput{Namespaces: []string{"dev"}}, "namespaces"},
		"nothing":                 {"namespace", getRightsizingInput{}, "needs a namespace"},
	} {
		_, err := rightsizingScanNamespaces(tc.scope, tc.input)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}

	got, err := rightsizingScanNamespaces("namespace", getRightsizingInput{Namespaces: []string{" dev", "staging", "dev"}})
	if err != nil || !reflect.DeepEqual(got, []string{"dev", "staging"}) {
		t.Errorf("namespaces = %v, %v; want trimmed and deduplicated", got, err)
	}

	_, _, err = handleGetRightsizing(context.Background(), nil, getRightsizingInput{Scope: "workload", Kind: "Deployment", Name: "api", Namespace: "dev", Namespaces: []string{"dev"}})
	if err == nil || !strings.Contains(err.Error(), "namespaces") {
		t.Errorf("scope=workload must reject namespaces, got %v", err)
	}
}

func TestExcludedRequestedNamespacesNamesEachDroppedName(t *testing.T) {
	if got := excludedRequestedNamespaces([]string{"dev"}, nil); got != nil {
		t.Errorf("an unrestricted identity excludes nothing, got %+v", got)
	}
	got := excludedRequestedNamespaces([]string{"dev", "autopush", "staging"}, []string{"dev", "staging"})
	if !reflect.DeepEqual(got, []excludedNamespace{{Name: "autopush", Reason: "access_denied"}}) {
		t.Errorf("excluded = %+v", got)
	}
}

func TestNamespacesWithoutWorkloadsNeedsAFullyCoveredScan(t *testing.T) {
	workloads := []prometheuspkg.RightsizingScanWorkload{{Namespace: "dev"}}
	if got := namespacesWithoutWorkloads([]string{"dev", "stagin"}, workloads, nil); !reflect.DeepEqual(got, []string{"stagin"}) {
		t.Errorf("empty namespaces = %v", got)
	}
	// A namespace holding only DaemonSets that match no node holds workloads;
	// calling it empty sends the agent looking for a typo that is not there.
	if got := namespacesWithoutWorkloads([]string{"dev", "gpu-operator"}, workloads, []string{"gpu-operator"}); len(got) != 0 {
		t.Errorf("a namespace of skipped DaemonSets is not empty, got %v", got)
	}
	if !scanCoveredEveryWorkload(prometheuspkg.RightsizingScanCoverage{WorkloadsDiscovered: 3, WorkloadsEvaluated: 3}) {
		t.Error("a complete scan can name empty namespaces")
	}
	for name, coverage := range map[string]prometheuspkg.RightsizingScanCoverage{
		"deadline":        {WorkloadsDiscovered: 3, WorkloadsEvaluated: 2},
		"restricted kind": {RestrictedKinds: []string{"Deployment"}},
		"partial cache":   {PartiallyCachedKinds: []string{"DaemonSet"}},
	} {
		if scanCoveredEveryWorkload(coverage) {
			t.Errorf("%s: an absent namespace may just be unread", name)
		}
	}
}

func TestScanGuidanceNamesNamespaceGapsDaemonSetsAndOwnership(t *testing.T) {
	got := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "namespace", reason: reasonNamespacesExcluded,
		coverage:                   &prometheuspkg.RightsizingScanCoverage{DaemonSetsWithoutNodes: 25},
		excludedNamespaces:         []excludedNamespace{{Name: "autopush", Reason: "access_denied"}},
		namespacesWithoutWorkloads: []string{"stagin"},
		workloadsShown:             true,
	})
	for _, want := range []string{
		"autopush (access_denied)",
		"stagin had no workload to scan",
		"25 DaemonSet(s) match no node",
		"A missing managedBy does not mean unmanaged",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "not the whole cluster") {
		t.Errorf("a namespace list is what was asked, not a narrowed cluster scan: %q", got)
	}
	if rightsizingRemediation(reasonNamespacesExcluded) == "" {
		t.Error("the excluded-namespaces reason needs remediation")
	}
}

func TestSplitByKindAccessExcludesNamespacesNoKindCanList(t *testing.T) {
	kept, denied := splitByKindAccess([]string{"dev", "locked"}, map[string][]string{
		"Deployment":  {"dev"},
		"StatefulSet": {"dev"},
	})
	if !reflect.DeepEqual(kept, []string{"dev"}) || !reflect.DeepEqual(denied, []excludedNamespace{{Name: "locked", Reason: "access_denied"}}) {
		t.Errorf("kept=%v denied=%+v", kept, denied)
	}
	// A kind listable everywhere covers every requested namespace.
	kept, denied = splitByKindAccess([]string{"dev", "locked"}, map[string][]string{"Deployment": {"dev"}, "DaemonSet": nil})
	if len(kept) != 2 || denied != nil {
		t.Errorf("a nil per-kind list covers all namespaces, got kept=%v denied=%+v", kept, denied)
	}
	// Every kind denied everywhere leaves nothing scanned.
	if kept, denied = splitByKindAccess([]string{"dev"}, map[string][]string{}); len(kept) != 0 || len(denied) != 1 {
		t.Errorf("no listable kind, got kept=%v denied=%+v", kept, denied)
	}
}

func TestEmptyScanReasonsAreOverriddenByANarrowedScope(t *testing.T) {
	for _, reason := range []string{"no_workloads", reasonOnlyDaemonSetsWithoutNodes} {
		if !scanClaimsFullCoverage(reason) {
			t.Errorf("%s claims the whole scope was read and must yield to a narrowed-scope reason", reason)
		}
	}
	if scanClaimsFullCoverage("some_evidence_unavailable") {
		t.Error("a reason about evidence is not a coverage claim")
	}
}

func TestQueryErrorGuidanceSeparatesRowsPastTheLimit(t *testing.T) {
	got := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster", reason: "some_evidence_unavailable",
		coverage: &prometheuspkg.RightsizingScanCoverage{}, omitted: rightsizingOmissions{QueryError: 94},
		returnedQueryErrors: 20, truncatedQueryErrors: 28,
	})
	if !strings.Contains(got, "142 row(s) failed their usage query (94 in omitted.queryError, 20 returned with queryError, 28 on workloads past the limit)") {
		t.Errorf("guidance must not call truncated rows returned: %q", got)
	}
}

func TestSkippedDaemonSetGuidanceKeepsTheScaledDownPoolCase(t *testing.T) {
	got := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanComplete, scope: "cluster", reason: reasonOnlyDaemonSetsWithoutNodes,
		coverage: &prometheuspkg.RightsizingScanCoverage{DaemonSetsWithoutNodes: 2},
	})
	if !strings.Contains(got, "match no node right now") || !strings.Contains(got, "still has history") {
		t.Errorf("a pool scaled to zero is not a DaemonSet that never runs: %q", got)
	}
	if remediation := rightsizingRemediation(reasonOnlyDaemonSetsWithoutNodes); !strings.Contains(remediation, "holds workloads") {
		t.Errorf("remediation must not say no workloads were found: %q", remediation)
	}
}

// A container with a failed query is left out of classification so it cannot
// demote the others, but it cannot be vouched for either: the workload is not
// "in range" while one of its containers was never judged.
func TestUnjudgedContainerKeepsTheWorkloadOutOfInRange(t *testing.T) {
	balanced := rightsizingRow(prometheuspkg.FitBalanced, 1, 1)
	balanced.RecommendedReq = nil
	balanced.RecommendedRequestValue = nil
	failed := rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 0)
	failed.Container = "sidecar"
	failed.RecommendedReq = nil
	failed.QueryError = prometheuspkg.RowUsageQueryFailed
	if got := prometheuspkg.ClassifyWorkloadRows([]prometheuspkg.RightsizingRow{balanced, failed}, 2, false); got != prometheuspkg.ClassNeedData {
		t.Errorf("classification = %q, want need_data", got)
	}
}

func TestReturnedShortHistoryRowsAreNamedAsACause(t *testing.T) {
	got := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster", reason: reasonRowEvidenceIncomplete,
		coverage: &prometheuspkg.RightsizingScanCoverage{}, returnedShortHistory: 2,
	})
	if !strings.Contains(got, "2 returned row(s) had too little history to judge") {
		t.Errorf("a short-history row kept beside its container's recommendation is a cause: %q", got)
	}
}

func TestShortHistoryPastTheLimitIsNamedAsACause(t *testing.T) {
	got := rightsizingGuidance(rightsizingGuidanceInput{
		state: prometheuspkg.RightsizingScanPartial, scope: "cluster", reason: reasonRowEvidenceIncomplete,
		coverage: &prometheuspkg.RightsizingScanCoverage{}, truncatedShortHistory: 3,
	})
	if !strings.Contains(got, "3 row(s) with too little history sit on workloads past the limit") {
		t.Errorf("truncated short-history rows are still a cause: %q", got)
	}
}

func TestEarlyUnavailableResponsesCarryGuidance(t *testing.T) {
	out := rightsizingUnavailable("workload", "prometheus_unavailable")
	if !strings.Contains(out.Guidance, "State is unavailable") {
		t.Errorf("every response explains its state in guidance, got %q", out.Guidance)
	}
}

func TestSkippedDaemonSetNamesAreCappedWithTheCountKept(t *testing.T) {
	names := make([]string, skippedDaemonSetsMax+5)
	for i := range names {
		names[i] = fmt.Sprintf("kube-system/ds-%03d", i)
	}
	kept, truncated := truncateRows(names, skippedDaemonSetsMax)
	if len(kept) != skippedDaemonSetsMax || !truncated {
		t.Errorf("kept %d truncated %v, want %d and true", len(kept), truncated, skippedDaemonSetsMax)
	}
}
