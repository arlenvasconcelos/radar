package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
)

// The Costs UI projects monthly spend as the hourly rate times this constant;
// the tool emits the projection server-side so an agent reporting a monthly
// figure to a user cannot disagree with the screen next to them.
const monthlyProjectionHours = 730

const (
	costDefaultLimit  = 20
	costMaxLimit      = 100
	costCallBudget    = 45 * time.Second
	costRateExplainer = "Costs are hourly rates. projectedMonthlyCost = hourlyCost x 730."
	// The denominator is allocation — max(requested, observed) — not the
	// request, so the ratio caps at 100 and a low value is not by itself a
	// defect. Neither fact is derivable from the numbers in the response.
	costEfficiencyExplainer = "efficiency compares observed use against cost allocation, which is the greater of requested and observed usage — not against the request. It therefore caps at 100, and a low value is not by itself a defect or recoverable money (bursty workloads, P95-sized requests and HPA headroom all read low). Use get_rightsizing to judge whether a request should change; it applies OOM and HPA gating a ratio cannot."
	// Enumerating what the workload total leaves out would go stale: the
	// namespace row's basis is the cost source's own total, which carries
	// storage, network, shared and external cost on Kubecost.
	costWorkloadTotalExplainer = "totals here sum CPU and memory allocation only, so they do not reconcile with this namespace's row in view=summary, whose basis is whatever the cost source totals."
	// With a kind/name selector the rows are not a truncation of the namespace,
	// so the namespace total would be read as the workload's own spend.
	costSelectedWorkloadTotalExplainer = "totals cover only the workload named by kind and name, not the namespace — call view=workloads without kind/name for the namespace's spend."
	// The Prometheus path returns no_metrics both when the source is healthy
	// with nothing in scope and when no cost source is installed at all, so this
	// text must not assert either one.
	costScopeEmptyRemediation = "No allocation was reported for the namespaces in namespaceScope. Either the cost source is healthy and that scope holds nothing (no workloads running, or a narrower scope than expected), or no cost source is installed yet — the two are indistinguishable here. Check whether OpenCost or Kubecost is running before telling the user to install it."
	costRequestedViewFmt      = "Totals cover only namespace %s, not the whole cluster."
	costPartialViewFmt        = "Totals cover only the %d namespace(s) this identity can read, not the whole cluster."
	// A pin and an RBAC limit produce the same namespace list. Reporting the
	// pin as a permission problem would have an agent tell a cluster-admin
	// their access is restricted.
	costPinnedViewFmt = "Totals cover only namespace %s, which radar is pinned to with --namespace-scope — not the whole cluster, and not a limit on this identity's permissions."
	// Kubecost's idle includes the __idle__ unallocated-node allocation while
	// hourlyCost is allocated spend only, so idle can legitimately exceed it.
	// An agent reads that as a bug in Radar unless the response says otherwise.
	costIdleExceedsAllocatedExplainer = "idleCost exceeds hourlyCost because hourlyCost is allocated spend while this source's idle also covers unallocated node capacity — the two are not a part and its whole, and idle is not an error."
	costWorkloadNotFoundRemediation   = "The cost source answered for this namespace but reported no allocation for the requested workload. Check the kind and name, or call view=workloads without kind/name to see what the namespace does have."
	costTrendSeriesExplainer          = "Series values are hourly rates at each point, not cumulative spend. Each series and the top-level total carry start, end and changePercent so growth can be read without summing the points."
)

// ReasonWorkloadNotFound is Radar's own reason: the cost source was healthy and
// the namespace had rows, but none matched the requested workload.
const reasonWorkloadNotFound = "workload_not_found"

// reasonCostDeadlineExceeded marks a call the budget cut short, so a slow cost
// source is not reported as a broken one.
const reasonCostDeadlineExceeded = "cost_deadline_exceeded"

// Cost figures are floats accumulated across rows, so they arrive as
// 0.14640000000000006. Agents echo them verbatim into user-facing answers.
func roundHourly(v float64) float64  { return math.Round(v*1e4) / 1e4 }
func roundMonthly(v float64) float64 { return math.Round(v*100) / 100 }

type getCostInput struct {
	View      string `json:"view,omitempty" jsonschema:"summary (default) for cluster totals plus per-namespace spend, workloads for one namespace broken down by workload (requires namespace), nodes for per-node spend, trend for spend over time"`
	Namespace string `json:"namespace,omitempty" jsonschema:"required for view=workloads; filters summary and trend to one namespace"`
	Range     string `json:"range,omitempty" jsonschema:"trend only: 6h, 24h (default), or 7d"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max rows for summary/workloads/nodes (default 20, max 100)"`
	Kind      string `json:"kind,omitempty" jsonschema:"with view=workloads only: return just this workload (Deployment, StatefulSet, DaemonSet, ...) instead of the namespace's top spenders; requires name"`
	Name      string `json:"name,omitempty" jsonschema:"with view=workloads only: the workload name; requires kind"`
}

type costTotals struct {
	HourlyCost           float64 `json:"hourlyCost"`
	ProjectedMonthlyCost float64 `json:"projectedMonthlyCost"`
	StorageCost          float64 `json:"storageCost,omitempty"`
	NetworkCost          float64 `json:"networkCost,omitempty"`
	IdleCost             float64 `json:"idleCost,omitempty"`
	// Pointer, not omitempty: a fully idle cluster measures 0% efficiency,
	// and dropping that reads as "efficiency was not reported".
	ClusterEfficiency *float64 `json:"clusterEfficiency"`
}

type namespaceCostRow struct {
	Name        string  `json:"name"`
	Kind        string  `json:"kind,omitempty"`
	Namespace   string  `json:"namespace,omitempty"`
	HourlyCost  float64 `json:"hourlyCost"`
	CPUCost     float64 `json:"cpuCost"`
	MemoryCost  float64 `json:"memoryCost"`
	StorageCost float64 `json:"storageCost,omitempty"`
	NetworkCost float64 `json:"networkCost,omitempty"`
	IdleCost    float64 `json:"idleCost,omitempty"`
	// Pointer, not omitempty: 0% is a measurement on a row using nothing.
	Efficiency       *float64 `json:"efficiency"`
	UsageUnavailable bool     `json:"usageUnavailable,omitempty"`
}

type workloadCostRow struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	Replicas   int     `json:"replicas"`
	HourlyCost float64 `json:"hourlyCost"`
	CPUCost    float64 `json:"cpuCost"`
	MemoryCost float64 `json:"memoryCost"`
	IdleCost   float64 `json:"idleCost,omitempty"`
	// Pointer, not omitempty: 0% is a measurement on a row using nothing.
	Efficiency       *float64 `json:"efficiency"`
	UsageUnavailable bool     `json:"usageUnavailable,omitempty"`
}

// nodeCostRow deliberately omits the per-node CPU and memory components. The
// OpenCost path reports them as per-vCPU-hour and per-GiB-hour unit prices
// while Kubecost reports whole-node totals, so one field name carries two
// different quantities. hourlyCost, instanceType and region answer "which
// nodes cost most" without that ambiguity.
type nodeCostRow struct {
	Name         string  `json:"name"`
	InstanceType string  `json:"instanceType,omitempty"`
	Region       string  `json:"region,omitempty"`
	HourlyCost   float64 `json:"hourlyCost"`
}

// costTrendSummary is the question an agent asks of a trend — did spend grow —
// answered server-side. The raw points remain, but summing 9 series across 25
// timestamps to get there is work the response can do once.
type costTrendSummary struct {
	Namespace     string   `json:"namespace,omitempty"`
	Start         float64  `json:"start"`
	End           float64  `json:"end"`
	ChangePercent *float64 `json:"changePercent,omitempty"`
	Points        int      `json:"points"`
}

type costTrendPoint struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"value"`
}

type costTrendSeriesDTO struct {
	Namespace     string           `json:"namespace"`
	Start         float64          `json:"start"`
	End           float64          `json:"end"`
	ChangePercent *float64         `json:"changePercent,omitempty"`
	DataPoints    []costTrendPoint `json:"dataPoints"`
}

type costResponse struct {
	View           string               `json:"view"`
	Available      bool                 `json:"available"`
	Reason         string               `json:"reason,omitempty"`
	Remediation    string               `json:"remediation,omitempty"`
	Source         string               `json:"source,omitempty"`
	Currency       string               `json:"currency"`
	Window         string               `json:"window,omitempty"`
	Range          string               `json:"range,omitempty"`
	DataThrough    string               `json:"dataThrough,omitempty"`
	Namespace      string               `json:"namespace,omitempty"`
	NamespaceScope []string             `json:"namespaceScope,omitempty"`
	Totals         *costTotals          `json:"totals,omitempty"`
	Namespaces     []namespaceCostRow   `json:"namespaces,omitempty"`
	Workloads      []workloadCostRow    `json:"workloads,omitempty"`
	Nodes          []nodeCostRow        `json:"nodes,omitempty"`
	Series         []costTrendSeriesDTO `json:"series,omitempty"`
	TrendTotal     *costTrendSummary    `json:"total,omitempty"`
	Truncated      bool                 `json:"truncated,omitempty"`
	// Row counts accompany truncated: without them the rows cannot be
	// reconciled against the totals, and "top 20 of N" is unsayable.
	NamespaceCount int    `json:"namespaceCount,omitempty"`
	WorkloadCount  int    `json:"workloadCount,omitempty"`
	NodeCount      int    `json:"nodeCount,omitempty"`
	Guidance       string `json:"guidance,omitempty"`
}

func handleGetCost(ctx context.Context, _ *mcp.CallToolRequest, input getCostInput) (*mcp.CallToolResult, any, error) {
	// MCP tool calls sit outside the router's 60s timeout group, and the
	// MCP prom client's 200s socket backstop is a hang guard, not a query
	// budget. Without this, a slow source holds the call for minutes past
	// the point the caller gave up. Same shape as the rightsizing scan budget.
	ctx, cancel := context.WithTimeout(ctx, costCallBudget)
	defer cancel()

	view := strings.ToLower(strings.TrimSpace(input.View))
	if view == "" {
		view = "summary"
	}
	// Normalize before validating: validating a trimmed value and dispatching
	// the raw one lets " 7d " pass the check and then resolve as 24h.
	input.Range = strings.TrimSpace(input.Range)

	limit := input.Limit
	if limit <= 0 {
		limit = costDefaultLimit
	}
	if limit > costMaxLimit {
		limit = costMaxLimit
	}

	// Silently dropping a parameter the caller set lets an agent believe a
	// filter applied. Reject the combination instead.
	if view != "trend" && input.Range != "" {
		return nil, nil, fmt.Errorf("range applies only to view=trend, not view=%s", view)
	}
	if view == "trend" && input.Limit > 0 {
		return nil, nil, errors.New("limit applies to the row views (summary, workloads, nodes), not view=trend")
	}
	input.Kind = strings.TrimSpace(input.Kind)
	input.Name = strings.TrimSpace(input.Name)
	if view != "workloads" && (input.Kind != "" || input.Name != "") {
		return nil, nil, fmt.Errorf("kind and name apply only to view=workloads, not view=%s", view)
	}
	if (input.Kind == "") != (input.Name == "") {
		return nil, nil, errors.New("kind and name go together — pass both to target one workload, or neither to rank the namespace's top spenders")
	}

	result, payload, err := costView(ctx, input, view, limit)
	// A response assembled after the budget expired is built from queries that
	// were cancelled mid-flight: the mandatory ones surface as query_error,
	// whose remediation sends the operator to check pod health for what is
	// really a slow query. Report the deadline instead of that wrong advice.
	if err == nil && ctx.Err() != nil {
		return toJSONResult(costResponse{
			View: view, Namespace: input.Namespace, Range: input.Range,
			Available: false, Reason: reasonCostDeadlineExceeded,
			Remediation: costRemediation(reasonCostDeadlineExceeded),
			Currency:    opencost.ResolveCurrency(),
		})
	}
	return result, payload, err
}

func costView(ctx context.Context, input getCostInput, view string, limit int) (*mcp.CallToolResult, any, error) {
	switch view {
	case "summary":
		return costSummaryView(ctx, input, limit)
	case "workloads":
		return costWorkloadsView(ctx, input, limit)
	case "nodes":
		if strings.TrimSpace(input.Namespace) != "" {
			return nil, nil, fmt.Errorf("view=nodes is cluster-wide and takes no namespace — use view=summary with a namespace, or view=workloads, for namespace-scoped spend")
		}
		return costNodesView(ctx, limit)
	case "trend":
		if !pkgopencost.SupportedTrendRange(input.Range) {
			return nil, nil, fmt.Errorf("unsupported range %q — use 6h, 24h (default), or 7d; anything else would silently return 24h", input.Range)
		}
		return costTrendView(ctx, input)
	default:
		return nil, nil, fmt.Errorf("unknown view %q — use summary (cluster totals + per-namespace), workloads (one namespace, requires namespace), nodes, or trend", input.View)
	}
}

func costSummaryView(ctx context.Context, input getCostInput, limit int) (*mcp.CallToolResult, any, error) {
	base := costResponse{View: "summary", Namespace: input.Namespace}

	requested := requestedNamespaces(input.Namespace)
	allowed := scopedNamespacesForUser(ctx, requested)
	if allowed != nil && len(allowed) == 0 {
		return toJSONResult(base.unavailable(deniedScopeReason(requested), opencost.ResolveCurrency(), ""))
	}

	// Selected() first on the success path: currency detection consults the
	// selected source. The denial paths above resolve it directly — an
	// explicit --opencost-currency override short-circuits detection, and the
	// undetectable cases return the default without caching it.
	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, ""))
	}

	var summary *pkgopencost.CostSummary
	if connection.Source == opencost.SourceKubecost {
		summary, err = pkgopencost.ComputeKubecostSummary(ctx, connection.Client, pkgopencost.KubecostCurrentOptions{
			Currency: currency, ClusterID: connection.ClusterID,
		})
		if err != nil {
			log.Printf("[mcp] Kubecost summary failed: %s", k8s.SanitizeForLog(err.Error()))
			return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"))
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return toJSONResult(base.unavailable(reason, currency, "prometheus"))
		}
		summary = pkgopencost.ComputeCostSummaryFromProm(ctx, client, pkgopencost.SummaryOptions{Currency: currency})
		summary.Source = "prometheus"
	}

	// Reuse the REST filter rather than re-deriving it: it also recomputes the
	// totals and efficiency from the surviving rows, so a partially-authorized
	// caller never sees cluster-wide spend.
	if allowed != nil {
		opencost.FilterCostSummary(summary, allowed)
	}

	resp := base
	resp.Available = summary.Available
	resp.Reason = summary.Reason
	resp.Source = summary.Source
	resp.Currency = summary.Currency
	resp.Window = summary.Window
	resp.DataThrough = summary.DataThrough
	resp.NamespaceScope = summary.NamespaceScope
	if !summary.Available {
		// FilterCostSummary reports a filtered-to-empty result as no_metrics,
		// which otherwise renders as "install a cost source" for a source that
		// is healthy and simply has no rows in the caller's namespaces.
		if scopedEmptyResult(summary.Reason, summary.NamespaceScope) {
			resp.Remediation = costScopeEmptyRemediation
			resp.Guidance = costGuidance(summary.NamespaceScope, strings.TrimSpace(input.Namespace))
		} else {
			resp.Remediation = costRemediation(summary.Reason)
		}
		return toJSONResult(resp)
	}

	resp.Totals = hourlyTotals(summary.TotalHourlyCost)
	resp.Totals.StorageCost = roundHourly(summary.TotalStorageCost)
	resp.Totals.NetworkCost = roundHourly(summary.TotalNetworkCost)
	resp.Totals.IdleCost = roundHourly(summary.TotalIdleCost)
	// Cluster efficiency is derived only from rows that HAVE usage evidence, so
	// when none of them do the 0 it lands on is an absence, not a measurement.
	resp.Totals.ClusterEfficiency = measuredEfficiency(summary.ClusterEfficiency, allUsageUnavailable(summary.Namespaces))
	resp.NamespaceCount = len(summary.Namespaces)
	rows, truncated := truncateRows(summary.Namespaces, limit)
	resp.Truncated = truncated
	resp.Namespaces = make([]namespaceCostRow, 0, len(rows))
	for _, row := range rows {
		resp.Namespaces = append(resp.Namespaces, namespaceCostRow{
			Name:             row.Name,
			Kind:             row.Kind,
			Namespace:        row.Namespace,
			HourlyCost:       roundHourly(row.HourlyCost),
			CPUCost:          roundHourly(row.CPUCost),
			MemoryCost:       roundHourly(row.MemoryCost),
			StorageCost:      roundHourly(row.StorageCost),
			NetworkCost:      roundHourly(row.NetworkCost),
			IdleCost:         roundHourly(row.IdleCost),
			Efficiency:       measuredEfficiency(row.Efficiency, row.UsageUnavailable),
			UsageUnavailable: row.UsageUnavailable,
		})
	}
	resp.Guidance = costGuidance(summary.NamespaceScope, strings.TrimSpace(input.Namespace)) + " " + costEfficiencyExplainer +
		partialUsageGuidance(summary.Namespaces) + idleGuidance(resp.Totals)
	return toJSONResult(resp)
}

// partialUsageGuidance fires when only SOME rows carry usage evidence. The
// aggregates are then computed over that subset and labelled as cluster
// figures, and a truncated row list can hide the rows they left out.
func partialUsageGuidance(rows []pkgopencost.NamespaceCost) string {
	missing := 0
	for _, row := range rows {
		if row.UsageUnavailable {
			missing++
		}
	}
	if missing == 0 || missing == len(rows) {
		return ""
	}
	return fmt.Sprintf(" %d of %d namespaces reported no usage evidence, so clusterEfficiency and idleCost cover only the rest — they are not whole-cluster figures, and the rows they exclude may be past the returned list.", missing, len(rows))
}

// allUsageUnavailable reports whether every row lacked usage evidence, which is
// the only case where the aggregate has nothing to average over.
func allUsageUnavailable(rows []pkgopencost.NamespaceCost) bool {
	if len(rows) == 0 {
		return true
	}
	for _, row := range rows {
		if !row.UsageUnavailable {
			return false
		}
	}
	return true
}

// idleGuidance fires only when the numbers themselves look contradictory, so a
// source whose idle is a subset of allocated spend carries no extra text.
func idleGuidance(totals *costTotals) string {
	if totals == nil || totals.IdleCost <= totals.HourlyCost {
		return ""
	}
	return " " + costIdleExceedsAllocatedExplainer
}

func costWorkloadsView(ctx context.Context, input getCostInput, limit int) (*mcp.CallToolResult, any, error) {
	namespace := strings.TrimSpace(input.Namespace)
	if namespace == "" {
		return nil, nil, fmt.Errorf("view=workloads needs a namespace — use view=summary first to find the expensive namespaces, then pass one here")
	}

	base := costResponse{View: "workloads", Namespace: namespace}

	if allowed := scopedNamespacesForUser(ctx, []string{namespace}); allowed != nil && len(allowed) == 0 {
		return toJSONResult(base.unavailable(deniedScopeReason([]string{namespace}), opencost.ResolveCurrency(), ""))
	}

	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, ""))
	}

	var workloads *pkgopencost.WorkloadCostResponse
	if connection.Source == opencost.SourceKubecost {
		workloads, err = pkgopencost.ComputeKubecostWorkloads(ctx, connection.Client, namespace, pkgopencost.KubecostCurrentOptions{
			Currency: currency, ClusterID: connection.ClusterID, Owners: opencost.BuildPodOwnerLookup(namespace),
		})
		if err != nil {
			log.Printf("[mcp] Kubecost workloads failed for namespace %q: %s", k8s.SanitizeForLog(namespace), k8s.SanitizeForLog(err.Error()))
			return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"))
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return toJSONResult(base.unavailable(reason, currency, "prometheus"))
		}
		workloads = pkgopencost.ComputeWorkloadsFromProm(ctx, client, namespace, opencost.BuildPodOwnerLookup(namespace))
		workloads.Currency = currency
		workloads.Source = "prometheus"
	}

	resp := base
	resp.Available = workloads.Available
	resp.Reason = workloads.Reason
	resp.Source = workloads.Source
	resp.Currency = workloads.Currency
	resp.DataThrough = workloads.DataThrough
	// The Prometheus path leaves Window empty while summary reports "1h", so
	// the agent could not state the window its numbers cover.
	resp.Window = workloads.Window
	if resp.Window == "" {
		resp.Window = pkgopencost.DefaultCurrentWindow
	}
	if !workloads.Available {
		// Same remap summary already applies: a healthy source with no rows in
		// the caller's scope is an empty scope, not a missing installation.
		if scopedEmptyResult(workloads.Reason, []string{namespace}) {
			resp.Remediation = costScopeEmptyRemediation
		} else {
			resp.Remediation = costRemediation(workloads.Reason)
		}
		return toJSONResult(resp)
	}

	// The ranking path's totals describe the namespace, so they are summed
	// before truncation narrows the rows. A kind/name selector replaces them
	// below, where the rows answer a different question than "top spenders".
	resp.Totals = hourlyTotals(sumWorkloadHourly(workloads.Workloads))
	resp.WorkloadCount = len(workloads.Workloads)

	selected := workloads.Workloads
	if input.Kind != "" {
		// Match before truncation: a workload ranked past limit is otherwise
		// unreachable, and the agent cannot tell "not found" from "not top-N".
		selected = selectWorkloadCost(workloads.Workloads, input.Kind, input.Name)
		// Totals describe what the caller asked for. Left namespace-wide, the
		// one row returned here sits beside a total covering every workload in
		// the namespace, and the agent reports that as the workload's spend.
		// The ranking path keeps its namespace totals: those rows ARE a
		// truncation of the namespace, so the total is the whole they came from.
		resp.Totals = hourlyTotals(sumWorkloadHourly(selected))
		resp.WorkloadCount = len(selected)
		if len(selected) == 0 {
			resp.Available = false
			resp.Reason = reasonWorkloadNotFound
			resp.Remediation = costWorkloadNotFoundRemediation
			resp.Totals = nil
			resp.Guidance = costRateExplainer
			return toJSONResult(resp)
		}
	}

	rows, truncated := truncateRows(selected, limit)
	resp.Truncated = truncated
	resp.Workloads = make([]workloadCostRow, 0, len(rows))
	for _, row := range rows {
		resp.Workloads = append(resp.Workloads, workloadCostRow{
			Name:             row.Name,
			Kind:             row.Kind,
			Replicas:         row.Replicas,
			HourlyCost:       roundHourly(row.HourlyCost),
			CPUCost:          roundHourly(row.CPUCost),
			MemoryCost:       roundHourly(row.MemoryCost),
			IdleCost:         roundHourly(row.IdleCost),
			Efficiency:       measuredEfficiency(row.Efficiency, !row.CPUUsageAvailable || !row.MemoryUsageAvailable),
			UsageUnavailable: !row.CPUUsageAvailable || !row.MemoryUsageAvailable,
		})
	}
	totalsExplainer := costWorkloadTotalExplainer
	if input.Kind != "" {
		totalsExplainer = costSelectedWorkloadTotalExplainer
	}
	resp.Guidance = costRateExplainer + " " + totalsExplainer + " " + costEfficiencyExplainer
	return toJSONResult(resp)
}

// sumWorkloadHourly totals the rows it is given, so the namespace total and a
// selector's total are the same arithmetic over different row sets.
func sumWorkloadHourly(rows []pkgopencost.WorkloadCost) float64 {
	var total float64
	for _, row := range rows {
		total += row.HourlyCost
	}
	return total
}

// selectWorkloadCost matches on kind and name case-insensitively: an agent
// carrying a kind from another tool's output may spell it "deployment".
func selectWorkloadCost(rows []pkgopencost.WorkloadCost, kind, name string) []pkgopencost.WorkloadCost {
	var matched []pkgopencost.WorkloadCost
	for _, row := range rows {
		if strings.EqualFold(row.Kind, kind) && strings.EqualFold(row.Name, name) {
			matched = append(matched, row)
		}
	}
	return matched
}

// scopedEmptyResult separates a healthy source with nothing in the caller's
// scope from a source that is not installed. Both arrive as no_metrics, and
// only the second one has anything to remediate.
func scopedEmptyResult(reason string, scope []string) bool {
	return reason == pkgopencost.ReasonNoMetrics && len(scope) > 0
}

func costNodesView(ctx context.Context, limit int) (*mcp.CallToolResult, any, error) {
	base := costResponse{View: "nodes"}

	if !canReadClusterScopedKind(ctx, "Node", "", "list") {
		return toJSONResult(base.unavailable(pkgopencost.ReasonAccessDenied, opencost.ResolveCurrency(), ""))
	}

	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, ""))
	}

	var nodes *pkgopencost.NodeCostResponse
	if connection.Source == opencost.SourceKubecost {
		nodes, err = pkgopencost.ComputeKubecostNodes(ctx, connection.Client, pkgopencost.KubecostCurrentOptions{
			Currency: currency, ClusterID: connection.ClusterID,
		})
		if err != nil {
			log.Printf("[mcp] Kubecost nodes failed: %s", k8s.SanitizeForLog(err.Error()))
			return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"))
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return toJSONResult(base.unavailable(reason, currency, "prometheus"))
		}
		nodes = pkgopencost.ComputeNodeCosts(ctx, client)
		nodes.Currency = currency
		nodes.Source = "prometheus"
	}

	resp := base
	resp.Available = nodes.Available
	resp.Reason = nodes.Reason
	resp.Source = nodes.Source
	resp.Currency = nodes.Currency
	resp.DataThrough = nodes.DataThrough
	resp.Window = nodes.Window
	if !nodes.Available {
		resp.Remediation = costRemediation(nodes.Reason)
		return toJSONResult(resp)
	}

	var total float64
	for _, row := range nodes.Nodes {
		total += row.HourlyCost
	}
	resp.Totals = hourlyTotals(total)
	resp.NodeCount = len(nodes.Nodes)

	rows, truncated := truncateRows(nodes.Nodes, limit)
	resp.Truncated = truncated
	resp.Nodes = make([]nodeCostRow, 0, len(rows))
	for _, row := range rows {
		resp.Nodes = append(resp.Nodes, nodeCostRow{
			Name:         row.Name,
			InstanceType: row.InstanceType,
			Region:       row.Region,
			HourlyCost:   roundHourly(row.HourlyCost),
		})
	}
	resp.Guidance = costRateExplainer + " Per-node CPU and memory components are deliberately not reported: the two cost sources define them differently, so use hourlyCost with instanceType to compare nodes."
	return toJSONResult(resp)
}

func costTrendView(ctx context.Context, input getCostInput) (*mcp.CallToolResult, any, error) {
	base := costResponse{View: "trend", Namespace: input.Namespace, Range: input.Range}

	requested := requestedNamespaces(input.Namespace)
	allowed := scopedNamespacesForUser(ctx, requested)
	if allowed != nil && len(allowed) == 0 {
		return toJSONResult(base.unavailable(deniedScopeReason(requested), opencost.ResolveCurrency(), ""))
	}

	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, ""))
	}

	var trend *pkgopencost.CostTrendResponse
	if connection.Source == opencost.SourceKubecost {
		trend, err = pkgopencost.ComputeKubecostTrend(ctx, connection.Client, pkgopencost.KubecostTrendOptions{
			Range: input.Range, Namespaces: allowed, Currency: currency, ClusterID: connection.ClusterID,
		})
		if err != nil {
			log.Printf("[mcp] Kubecost trend failed: %s", k8s.SanitizeForLog(err.Error()))
			return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"))
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return toJSONResult(base.unavailable(reason, currency, "prometheus"))
		}
		trend = pkgopencost.ComputeCostTrendFromProm(ctx, client, pkgopencost.TrendPromOptions{
			Range: input.Range, Namespaces: allowed,
		})
		trend.Currency = currency
		trend.Source = "prometheus"
	}

	resp := base
	resp.Available = trend.Available
	resp.Reason = trend.Reason
	resp.Source = trend.Source
	resp.Currency = trend.Currency
	resp.DataThrough = trend.DataThrough
	// Keep the echoed request: the backends return an empty Range on their
	// failure paths, and dropping it leaves an agent that asked for 7d unable
	// to tell which range failed.
	if trend.Range != "" {
		resp.Range = trend.Range
	}
	if allowed != nil {
		resp.NamespaceScope = allowed
	}
	resp.Guidance = costGuidance(allowed, strings.TrimSpace(input.Namespace))
	if !trend.Available {
		if scopedEmptyResult(trend.Reason, allowed) {
			resp.Remediation = costScopeEmptyRemediation
		} else {
			resp.Remediation = costRemediation(trend.Reason)
		}
		return toJSONResult(resp)
	}
	resp.Series, resp.TrendTotal = summarizeTrend(trend.Series)
	resp.Guidance += " " + costTrendSeriesExplainer
	return toJSONResult(resp)
}

// summarizeTrend answers "is spend growing" in the response. The raw points
// stay, but an agent should not have to sum nine series across 25 timestamps
// to read a direction, and it cannot do that arithmetic reliably.
func summarizeTrend(series []pkgopencost.CostTrendSeries) ([]costTrendSeriesDTO, *costTrendSummary) {
	if len(series) == 0 {
		return nil, nil
	}
	out := make([]costTrendSeriesDTO, 0, len(series))
	// Totals sum across series per timestamp, so a series that starts late
	// does not read as a cluster-wide drop.
	totalByTimestamp := map[int64]float64{}
	for _, s := range series {
		dto := costTrendSeriesDTO{Namespace: s.Namespace, DataPoints: make([]costTrendPoint, 0, len(s.DataPoints))}
		for _, point := range s.DataPoints {
			dto.DataPoints = append(dto.DataPoints, costTrendPoint{
				Timestamp: time.Unix(point.Timestamp, 0).UTC().Format(time.RFC3339),
				Value:     roundHourly(point.Value),
			})
			totalByTimestamp[point.Timestamp] += point.Value
		}
		if len(s.DataPoints) > 0 {
			dto.Start = roundHourly(s.DataPoints[0].Value)
			dto.End = roundHourly(s.DataPoints[len(s.DataPoints)-1].Value)
			dto.ChangePercent = changePercent(len(s.DataPoints), s.DataPoints[0].Value, s.DataPoints[len(s.DataPoints)-1].Value)
		}
		out = append(out, dto)
	}

	stamps := make([]int64, 0, len(totalByTimestamp))
	for stamp := range totalByTimestamp {
		stamps = append(stamps, stamp)
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })
	if len(stamps) == 0 {
		return out, nil
	}
	first, last := totalByTimestamp[stamps[0]], totalByTimestamp[stamps[len(stamps)-1]]
	return out, &costTrendSummary{
		Start:         roundHourly(first),
		End:           roundHourly(last),
		ChangePercent: changePercent(len(stamps), first, last),
		Points:        len(stamps),
	}
}

// changePercent is nil rather than zero when the starting value is zero: a
// percentage change from nothing is undefined, and reporting 0 would say spend
// held flat when it actually appeared.
func changePercent(points int, start, end float64) *float64 {
	// One point is not a change: start and end are the same sample, and the
	// resulting 0 reads as "spend held flat" over an interval never observed.
	if points < 2 || start == 0 {
		return nil
	}
	pct := math.Round((end-start)/start*1000) / 10
	return &pct
}

// promCostClient returns the reason a cost query cannot run, empty when it can.
// A missing client and a failed connection are different failures — collapsing
// them would tell a user with an auth error to go install OpenCost.
//
// PromForMCP, not Prom: the shared Prom() client carries a 10s socket backstop
// sized for REST callers, which a 7d trend query on a large cluster outruns.
func promCostClient(ctx context.Context) (*prom.Client, string) {
	client := prometheuspkg.GetClient()
	if client == nil {
		return nil, pkgopencost.ReasonNoPrometheus
	}
	if _, _, err := client.EnsureConnected(ctx); err != nil {
		log.Printf("[mcp] Prometheus EnsureConnected failed for cost query: %v", err)
		return nil, opencost.ConnectionFailureReason(err)
	}
	promClient := client.PromForMCP()
	if promClient == nil {
		return nil, pkgopencost.ReasonNoPrometheus
	}
	return promClient, ""
}

func requestedNamespaces(namespace string) []string {
	if strings.TrimSpace(namespace) == "" {
		return nil
	}
	return []string{strings.TrimSpace(namespace)}
}

// unavailable stamps a failure onto the response identity the view already
// built, so the scope fields (namespace, range) travel with every failure path
// instead of being re-stamped at each one — the omission that reads as a
// cluster-wide answer.
func (r costResponse) unavailable(reason, currency, source string) costResponse {
	r.Available = false
	r.Reason = reason
	r.Remediation = costRemediation(reason)
	r.Currency = currency
	if source != "" {
		r.Source = source
	}
	return r
}

// hourlyTotals keeps the monthly projection on one multiply: the constant must
// not disagree with the Costs UI, and four independent call sites drift.
func hourlyTotals(hourly float64) *costTotals {
	return &costTotals{
		HourlyCost:           roundHourly(hourly),
		ProjectedMonthlyCost: roundMonthly(hourly * monthlyProjectionHours),
	}
}

// measuredEfficiency separates "used nothing" from "we could not measure".
// Both are zero on the wire otherwise, and only the second is missing data.
func measuredEfficiency(value float64, unavailable bool) *float64 {
	if unavailable {
		return nil
	}
	rounded := roundHourly(value)
	return &rounded
}

func truncateRows[T any](rows []T, limit int) ([]T, bool) {
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

// deniedScopeReason keeps the pin out of the RBAC bucket: "you cannot read
// this" and "radar was started with --namespace" need different answers.
func deniedScopeReason(requested []string) string {
	if reason := DeniedScopeReason(requested); reason != "" {
		return reason
	}
	return pkgopencost.ReasonAccessDenied
}

func costRemediation(reason string) string {
	switch reason {
	case ReasonOutsideNamespaceScope:
		if pinned, ok := NamespacePinned(); ok {
			return fmt.Sprintf("radar is pinned to namespace %s with --namespace-scope, so it cannot report on the requested scope. This is a startup flag, not a permissions problem — restart radar without --namespace-scope for cluster-wide cost.", pinned)
		}
		return "radar is pinned to a single namespace with --namespace-scope, so it cannot report on the requested scope. This is a startup flag, not a permissions problem."
	case reasonWorkloadNotFound:
		return costWorkloadNotFoundRemediation
	case reasonCostDeadlineExceeded:
		return fmt.Sprintf("The cost request exceeded its %s budget before answering. The budget also covers permission discovery and source selection, so this does not establish that the cost source was reached — narrow the request (a namespace instead of the cluster, or a shorter trend range) and retry.", costCallBudget)
	case pkgopencost.ReasonNoPrometheus:
		return "No Prometheus found. Radar auto-discovers it, or start radar with --prometheus-url. Cost data additionally needs OpenCost or Kubecost installed."
	case pkgopencost.ReasonNoCostSource:
		return "Neither OpenCost metrics nor Kubecost was found. Install one of them in the cluster; OpenCost also needs a Prometheus for Radar to read it through."
	case pkgopencost.ReasonNoMetrics:
		// Kubecost answers with this reason too, so the text cannot name
		// Prometheus as the thing that is reachable. The source field says
		// which one answered.
		return "The cost source answered but reported no cost data. If OpenCost or Kubecost was just installed, let it scrape for a few minutes; if neither is installed, install one — the source field says which one Radar asked."
	case pkgopencost.ReasonAccessDenied:
		return "This identity cannot read the requested scope. Costs are reported only for namespaces (or nodes) its RBAC allows."
	case pkgopencost.ReasonAuthentication:
		return "The cost source rejected Radar's credentials — check the Kubecost API key in Radar's settings, or the Prometheus credentials if cost comes from OpenCost. The source field says which one answered."
	case pkgopencost.ReasonConfigMismatch:
		return "The configured cost source does not match the connected cluster. Check Radar's cost settings against the current kubeconfig context."
	case pkgopencost.ReasonDeploymentConfig:
		return "The cost source is installed but misconfigured. Check the OpenCost/Kubecost deployment's own configuration."
	case pkgopencost.ReasonInsufficientHistory, pkgopencost.ReasonHistoryUnsupported:
		return "The cost source has not retained enough history for this range. Try a shorter range, or wait for it to accumulate."
	case pkgopencost.ReasonNotFound:
		return "The requested resource no longer exists."
	case pkgopencost.ReasonQueryError, pkgopencost.ReasonSourceUnavailable:
		return "The cost source is installed but the query failed. Check that OpenCost/Kubecost pods are healthy."
	default:
		return ""
	}
}

// costGuidance distinguishes a scope the caller asked for from one RBAC imposed
// — reporting an explicit namespace filter as a permission limit would have an
// agent tell the user their access is restricted when they simply asked for one
// namespace.
func costGuidance(scope []string, requestedNamespace string) string {
	if len(scope) == 0 {
		return costRateExplainer
	}
	if requestedNamespace != "" {
		return costRateExplainer + " " + fmt.Sprintf(costRequestedViewFmt, requestedNamespace)
	}
	// The pin is checked before RBAC: both narrow the scope identically, and
	// only the pin is knowable from configuration rather than from the answer.
	if pinned, ok := NamespacePinned(); ok {
		return costRateExplainer + " " + fmt.Sprintf(costPinnedViewFmt, pinned)
	}
	return costRateExplainer + " " + fmt.Sprintf(costPartialViewFmt, len(scope))
}
