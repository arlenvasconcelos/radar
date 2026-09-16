package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
)

func TestGetCostRejectsUnknownView(t *testing.T) {
	_, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "spend"})
	if err == nil {
		t.Fatal("expected an error for an unknown view")
	}
	for _, want := range []string{"summary", "workloads", "nodes", "trend"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the %q view so the model can recover, got: %v", want, err)
		}
	}
}

func TestGetCostWorkloadsViewRequiresNamespace(t *testing.T) {
	_, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "workloads"})
	if err == nil {
		t.Fatal("expected an error when view=workloads has no namespace")
	}
	if !strings.Contains(err.Error(), "view=summary") {
		t.Errorf("error should route the model to summary to find a namespace, got: %v", err)
	}
}

func TestCostGuidanceFlagsPartialNamespaceScope(t *testing.T) {
	full := costGuidance(nil, "")
	if !strings.Contains(full, "730") {
		t.Errorf("guidance should state the monthly projection convention, got: %q", full)
	}
	if strings.Contains(full, "not the whole cluster") {
		t.Errorf("unscoped guidance should not claim a partial view, got: %q", full)
	}

	partial := costGuidance([]string{"a", "b"}, "")
	if !strings.Contains(partial, "2 namespace(s)") || !strings.Contains(partial, "not the whole cluster") {
		t.Errorf("scoped guidance should say totals cover only the readable namespaces, got: %q", partial)
	}

	// An explicit filter is the caller's own choice, not a permission limit.
	requested := costGuidance([]string{"prod"}, "prod")
	if !strings.Contains(requested, "only namespace prod") {
		t.Errorf("an explicitly requested namespace should be named, got: %q", requested)
	}
	if strings.Contains(requested, "this identity can read") {
		t.Errorf("an explicit filter must not be reported as an RBAC limitation, got: %q", requested)
	}
}

func TestCostRemediationExplainsMissingCostSource(t *testing.T) {
	if got := costRemediation(pkgopencost.ReasonNoPrometheus); !strings.Contains(got, "OpenCost") {
		t.Errorf("no_prometheus remediation should mention the cost source, got: %q", got)
	}
	if got := costRemediation(pkgopencost.ReasonNoMetrics); !strings.Contains(got, "OpenCost") {
		t.Errorf("no_metrics remediation should name what to install, got: %q", got)
	}
	if got := costRemediation("something_new"); got != "" {
		t.Errorf("unknown reasons should get no invented remediation, got: %q", got)
	}
}

func TestCostUnavailableCarriesRemediation(t *testing.T) {
	resp := costResponse{View: "summary"}.unavailable(pkgopencost.ReasonNoPrometheus, "USD", "")
	if resp.Available {
		t.Fatal("response should be unavailable")
	}
	if resp.Remediation == "" {
		t.Error("an unavailable response must say what is missing, or the agent reports the cluster has no cost data")
	}
	if resp.Currency != "USD" || resp.View != "summary" {
		t.Errorf("unexpected envelope: %+v", resp)
	}
}

func TestMonthlyProjectionMatchesCostsUI(t *testing.T) {
	if monthlyProjectionHours != 730 {
		t.Fatalf("the Costs UI projects hourly x 730; a different constant makes the tool disagree with the screen, got %d", monthlyProjectionHours)
	}
}

func TestGetCostNodesViewRejectsNamespace(t *testing.T) {
	_, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "nodes", Namespace: "prod"})
	if err == nil {
		t.Fatal("node spend is cluster-wide; silently dropping the namespace lets an agent report it as namespace-scoped")
	}
	if !strings.Contains(err.Error(), "view=summary") {
		t.Errorf("error should route to a namespace-capable view, got: %v", err)
	}
}

func TestGetCostAdvertisesOnlyServableTrendRanges(t *testing.T) {
	// pkg/opencost resolves 6h and 7d and silently defaults everything else to
	// 24h, so advertising any other value promises data the backends cannot serve.
	field, ok := reflect.TypeFor[getCostInput]().FieldByName("Range")
	if !ok {
		t.Fatal("getCostInput has no Range field")
	}
	schema := field.Tag.Get("jsonschema")
	for _, unservable := range []string{"30d", "90d", "1y"} {
		if strings.Contains(schema, unservable) {
			t.Errorf("range schema advertises %q, which resolveTrendRange silently turns into 24h: %q", unservable, schema)
		}
	}
	for _, servable := range []string{"6h", "24h", "7d"} {
		if !strings.Contains(schema, servable) {
			t.Errorf("range schema omits the servable range %q: %q", servable, schema)
		}
	}
}

func TestGetCostRejectsUnservableTrendRange(t *testing.T) {
	_, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "trend", Range: "30d"})
	if err == nil {
		t.Fatal("an unservable range must be rejected, not silently answered with 24h data")
	}
	for _, want := range []string{"6h", "24h", "7d"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the servable ranges, missing %q: %v", want, err)
		}
	}
	if !pkgopencost.SupportedTrendRange("") || !pkgopencost.SupportedTrendRange("7d") {
		t.Error("empty and 7d are servable")
	}
	if pkgopencost.SupportedTrendRange("30d") {
		t.Error("30d resolves to 24h in both backends and must not be accepted")
	}
}

func TestCostRemediationCoversReachableReasons(t *testing.T) {
	for _, reason := range []string{
		pkgopencost.ReasonNoPrometheus, pkgopencost.ReasonNoCostSource, pkgopencost.ReasonNoMetrics,
		pkgopencost.ReasonAccessDenied, pkgopencost.ReasonAuthentication, pkgopencost.ReasonQueryError,
		pkgopencost.ReasonSourceUnavailable, pkgopencost.ReasonConfigMismatch, pkgopencost.ReasonDeploymentConfig,
		pkgopencost.ReasonInsufficientHistory, pkgopencost.ReasonHistoryUnsupported, pkgopencost.ReasonNotFound,
	} {
		if costRemediation(reason) == "" {
			t.Errorf("reason %q reaches the model with no remediation", reason)
		}
	}
}

func TestGetCostRejectsViewIncompatibleParams(t *testing.T) {
	// Accepting a parameter a view ignores lets an agent believe it filtered.
	for _, input := range []getCostInput{
		{View: "summary", Range: "7d"},
		{View: "nodes", Range: "6h"},
		{View: "trend", Limit: 10},
	} {
		if _, _, err := handleGetCost(context.Background(), nil, input); err == nil {
			t.Errorf("%+v should be rejected rather than silently ignoring the parameter", input)
		}
	}
	// The compatible combinations must still work through validation.
	if _, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "trend", Range: "7d"}); err != nil {
		t.Errorf("trend accepts range: %v", err)
	}
}

func TestCostRemediationDistinguishesSources(t *testing.T) {
	// "no cost source at all" and "a source exists but has no metrics" need
	// different advice; sharing a string tells half of callers the wrong thing.
	noSource := costRemediation(pkgopencost.ReasonNoCostSource)
	noMetrics := costRemediation(pkgopencost.ReasonNoMetrics)
	if noSource == noMetrics {
		t.Error("no_cost_source and no_metrics describe different situations and must not share remediation")
	}
	if strings.Contains(noSource, "Prometheus is reachable") {
		t.Errorf("no_cost_source must not claim Prometheus is reachable: %q", noSource)
	}
	// Either source can reject credentials, so naming only one misdirects.
	auth := costRemediation(pkgopencost.ReasonAuthentication)
	if !strings.Contains(auth, "Kubecost") || !strings.Contains(auth, "Prometheus") {
		t.Errorf("authentication remediation should cover both sources: %q", auth)
	}
}

func TestEfficiencyGuidanceStatesTheDenominator(t *testing.T) {
	// An agent reading efficiency: 100 cannot tell "correctly sized" from
	// "consuming well above its request" — allocation is max(requested,
	// observed), so the ratio caps at 100. Nothing in the numbers says so.
	if !strings.Contains(costEfficiencyExplainer, "greater of requested and observed") {
		t.Error("guidance must state that the denominator is allocation, not the request")
	}
	if !strings.Contains(costEfficiencyExplainer, "caps at 100") {
		t.Error("guidance must state the ceiling, or 100 reads as a clean bill of health")
	}
	if !strings.Contains(costEfficiencyExplainer, "get_rightsizing") {
		t.Error("guidance must route the 'should this change' question to the tool that gates on OOM and HPA")
	}
}

func TestEfficiencyGuidanceRidesTheViewsThatEmitEfficiency(t *testing.T) {
	// summary carries clusterEfficiency plus per-namespace efficiency, and
	// workloads carries per-workload efficiency. nodes and trend carry neither,
	// so the explainer would be noise there.
	summary := costGuidance(nil, "") + " " + costEfficiencyExplainer
	if !strings.Contains(summary, costRateExplainer) || !strings.Contains(summary, costEfficiencyExplainer) {
		t.Error("summary guidance must carry both the rate and the efficiency explainer")
	}
}

func TestHourlyTotalsAreRounded(t *testing.T) {
	// Float accumulation across rows yields 0.14640000000000006, which agents
	// echo verbatim into user-facing answers.
	totals := hourlyTotals(0.0488 * 3)
	if totals.HourlyCost != 0.1464 {
		t.Errorf("hourly cost should be rounded to 4dp, got %v", totals.HourlyCost)
	}
	if totals.ProjectedMonthlyCost != 106.87 {
		t.Errorf("monthly projection should be rounded to 2dp, got %v", totals.ProjectedMonthlyCost)
	}
}

func TestIdleGuidanceFiresOnlyWhenIdleExceedsAllocated(t *testing.T) {
	// Kubecost's idle includes unallocated node capacity while hourlyCost is
	// allocated spend, so idle > total is correct and needs saying.
	exceeds := idleGuidance(&costTotals{HourlyCost: 0.0886, IdleCost: 0.2677})
	if !strings.Contains(exceeds, "unallocated") {
		t.Errorf("idle above allocated must be explained, got %q", exceeds)
	}
	if got := idleGuidance(&costTotals{HourlyCost: 1.0, IdleCost: 0.2}); got != "" {
		t.Errorf("a subset idle needs no caveat, got %q", got)
	}
	if got := idleGuidance(nil); got != "" {
		t.Errorf("no totals, no caveat, got %q", got)
	}
}

func TestNodeRowOmitsAmbiguousComponentCosts(t *testing.T) {
	// The OpenCost path reports per-vCPU-hour unit prices and Kubecost reports
	// whole-node totals under the same names, so neither is emitted.
	blob, err := json.Marshal(nodeCostRow{Name: "n1", HourlyCost: 0.0961})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"cpuCost", "memoryCost"} {
		if strings.Contains(string(blob), field) {
			t.Errorf("node rows must not carry %q — its meaning differs per cost source: %s", field, blob)
		}
	}
}

func TestSelectWorkloadCostMatchesBeforeTruncation(t *testing.T) {
	// A workload ranked past limit is otherwise unreachable: view=workloads has
	// no other selector and the list is spend-ranked.
	rows := []pkgopencost.WorkloadCost{
		{Name: "expensive", Kind: "Deployment", HourlyCost: 9},
		{Name: "checkout", Kind: "StatefulSet", HourlyCost: 0.01},
	}
	got := selectWorkloadCost(rows, "statefulset", "CHECKOUT")
	if len(got) != 1 || got[0].Name != "checkout" {
		t.Errorf("kind and name should match case-insensitively, got %+v", got)
	}
	if len(selectWorkloadCost(rows, "Deployment", "absent")) != 0 {
		t.Error("a name that is not present must not fall back to another row")
	}
}

func TestGetCostRejectsKindAndNameOutsideWorkloadsView(t *testing.T) {
	for _, input := range []getCostInput{
		{View: "summary", Kind: "Deployment", Name: "api"},
		{View: "nodes", Kind: "Deployment", Name: "api"},
		{View: "trend", Kind: "Deployment", Name: "api"},
	} {
		if _, _, err := handleGetCost(context.Background(), nil, input); err == nil {
			t.Errorf("%+v should be rejected rather than silently ignoring the selector", input)
		}
	}
	// Half a selector cannot match anything, so it is a caller error too.
	if _, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "workloads", Namespace: "prod", Kind: "Deployment"}); err == nil {
		t.Error("kind without name should be rejected")
	}
	if _, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "workloads", Namespace: "prod", Name: "api"}); err == nil {
		t.Error("name without kind should be rejected")
	}
}

func TestScopedEmptyResultSeparatesEmptyScopeFromMissingSource(t *testing.T) {
	if !scopedEmptyResult(pkgopencost.ReasonNoMetrics, []string{"prod"}) {
		t.Error("a healthy source with no rows in a named scope is an empty scope")
	}
	if scopedEmptyResult(pkgopencost.ReasonNoMetrics, nil) {
		t.Error("cluster-wide no_metrics really is a missing cost source")
	}
	if scopedEmptyResult(pkgopencost.ReasonNoPrometheus, []string{"prod"}) {
		t.Error("only no_metrics is ambiguous; a missing Prometheus is not")
	}
}

func TestSummarizeTrendPreComputesDirection(t *testing.T) {
	// Without this an agent has to sum every series per timestamp before it can
	// answer "is spend growing", which it cannot do reliably.
	series := []pkgopencost.CostTrendSeries{
		{Namespace: "a", DataPoints: []pkgopencost.CostDataPoint{{Timestamp: 100, Value: 1}, {Timestamp: 200, Value: 2}}},
		{Namespace: "b", DataPoints: []pkgopencost.CostDataPoint{{Timestamp: 100, Value: 3}, {Timestamp: 200, Value: 3}}},
	}
	out, total := summarizeTrend(series)

	if len(out) != 2 {
		t.Fatalf("every series should survive, got %d", len(out))
	}
	if out[0].ChangePercent == nil || *out[0].ChangePercent != 100 {
		t.Errorf("series a doubled, expected +100%%, got %v", out[0].ChangePercent)
	}
	if out[0].DataPoints[0].Timestamp != "1970-01-01T00:01:40Z" {
		t.Errorf("timestamps should be RFC3339, got %q", out[0].DataPoints[0].Timestamp)
	}
	if total == nil {
		t.Fatal("a top-level total is what answers the growth question")
	}
	// Summed per timestamp: 4 -> 5, not series-by-series.
	if total.Start != 4 || total.End != 5 {
		t.Errorf("totals must sum across series per timestamp, got %+v", total)
	}
	if total.ChangePercent == nil || *total.ChangePercent != 25 {
		t.Errorf("expected +25%%, got %v", total.ChangePercent)
	}
}

func TestChangePercentIsAbsentFromZero(t *testing.T) {
	// Reporting 0% from a zero start would say spend held flat when it in fact
	// appeared.
	if got := changePercent(2, 0, 5); got != nil {
		t.Errorf("a change from zero is undefined, got %v", *got)
	}
	if got := changePercent(2, 4, 3); got == nil || *got != -25 {
		t.Errorf("expected -25%%, got %v", got)
	}
	// A single sample spans no interval, so start and end are the same point.
	if got := changePercent(1, 4, 3); got != nil {
		t.Errorf("one point cannot express a change, got %v", *got)
	}
}

func TestCostRemediationNamesThePinRatherThanPermissions(t *testing.T) {
	// The pin and an RBAC denial produce the same namespace list; telling a
	// cluster-admin their access is restricted is the wrong answer.
	got := costRemediation(ReasonOutsideNamespaceScope)
	if !strings.Contains(got, "--namespace") {
		t.Errorf("remediation should name the flag, got %q", got)
	}
	if !strings.Contains(got, "not a permissions problem") {
		t.Errorf("remediation must not read as an RBAC denial, got %q", got)
	}
}

func TestWorkloadNotFoundIsDistinctFromAnEmptyNamespace(t *testing.T) {
	if got := costRemediation(reasonWorkloadNotFound); got == "" {
		t.Error("a no-match selector needs its own remediation, not an empty list")
	}
}

// With kind+name the rows are one workload rather than a truncation of the
// namespace, so a namespace-wide total sitting beside them is read as that
// workload's spend — the agent then reports the namespace's bill for one
// Deployment.
func TestSelectedWorkloadTotalsCoverOnlyTheSelection(t *testing.T) {
	rows := []pkgopencost.WorkloadCost{
		{Name: "checkout", Kind: "Deployment", HourlyCost: 2},
		{Name: "orders", Kind: "StatefulSet", HourlyCost: 3},
		{Name: "telemetry", Kind: "DaemonSet", HourlyCost: 5},
	}

	if got := sumWorkloadHourly(rows); got != 10 {
		t.Fatalf("namespace total should cover every workload, got %v", got)
	}

	selected := selectWorkloadCost(rows, "Deployment", "checkout")
	if got := sumWorkloadHourly(selected); got != 2 {
		t.Errorf("a selected workload's total must be its own cost, not the namespace's: %v", got)
	}
}

// The namespace explainer tells the agent the totals do not reconcile with the
// summary view's namespace row. Under a selector that sentence describes a
// total the response no longer carries.
func TestSelectedWorkloadGuidanceDoesNotClaimNamespaceTotals(t *testing.T) {
	if !strings.Contains(costSelectedWorkloadTotalExplainer, "not the namespace") {
		t.Errorf("selector guidance must say the totals exclude the namespace: %q", costSelectedWorkloadTotalExplainer)
	}
	if costSelectedWorkloadTotalExplainer == costWorkloadTotalExplainer {
		t.Error("the selector and ranking paths report different totals, so they cannot share one explainer")
	}
}

// A single data point has no interval, so start and end are the same sample.
// Reporting 0% there told the agent spend held flat over a window never seen.
func TestSinglePointTrendReportsNoChangePercent(t *testing.T) {
	series, total := summarizeTrend([]pkgopencost.CostTrendSeries{{
		Namespace:  "shop",
		DataPoints: []pkgopencost.CostDataPoint{{Timestamp: 1700000000, Value: 0.5}},
	}})
	if len(series) != 1 || series[0].ChangePercent != nil {
		t.Errorf("one point cannot state a change, got %+v", series)
	}
	if total == nil || total.ChangePercent != nil {
		t.Errorf("the cluster total must not claim flat spend from one point, got %+v", total)
	}
}

// Two points do carry a change, so the guard must not suppress a real one.
func TestTwoPointTrendStillReportsChangePercent(t *testing.T) {
	_, total := summarizeTrend([]pkgopencost.CostTrendSeries{{
		Namespace: "shop",
		DataPoints: []pkgopencost.CostDataPoint{
			{Timestamp: 1700000000, Value: 1},
			{Timestamp: 1700003600, Value: 2},
		},
	}})
	if total == nil || total.ChangePercent == nil || *total.ChangePercent != 100 {
		t.Fatalf("expected +100%%, got %+v", total)
	}
}

// A cluster whose namespaces all lack usage evidence has nothing to average,
// so the aggregate must read as unmeasured rather than as a measured 0%.
func TestClusterEfficiencyIsNullWhenNoNamespaceHasUsage(t *testing.T) {
	if !allUsageUnavailable([]pkgopencost.NamespaceCost{{Name: "a", UsageUnavailable: true}}) {
		t.Error("a row without usage evidence cannot support an aggregate")
	}
	if allUsageUnavailable([]pkgopencost.NamespaceCost{{Name: "a", UsageUnavailable: true}, {Name: "b"}}) {
		t.Error("one measured row is enough to report the aggregate")
	}
	// Zero is a measurement and must survive the wire.
	measured := measuredEfficiency(0, false)
	if measured == nil || *measured != 0 {
		t.Errorf("0%% efficiency is a measurement, got %v", measured)
	}
	if measuredEfficiency(0, true) != nil {
		t.Error("unavailable usage must report null, not zero")
	}
}

// The aggregate covers only the rows with usage evidence. When some rows have
// none, the response has to say so — the rows it left out may be past the cap.
func TestPartialUsageEvidenceIsNamedInGuidance(t *testing.T) {
	mixed := []pkgopencost.NamespaceCost{{Name: "a"}, {Name: "b", UsageUnavailable: true}}
	if !strings.Contains(partialUsageGuidance(mixed), "1 of 2 namespaces") {
		t.Errorf("a mixed result must name the missing rows, got %q", partialUsageGuidance(mixed))
	}
	// All-or-nothing is covered by clusterEfficiency going null, not by prose.
	if partialUsageGuidance([]pkgopencost.NamespaceCost{{Name: "a"}}) != "" {
		t.Error("a fully measured result needs no qualification")
	}
	if partialUsageGuidance([]pkgopencost.NamespaceCost{{Name: "a", UsageUnavailable: true}}) != "" {
		t.Error("a fully unmeasured result reports null efficiency instead")
	}
}
