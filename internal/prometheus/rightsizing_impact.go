package prometheus

import (
	"fmt"
	"math"
	"slices"
)

// Classification and impact mirror web/src/components/rightsizing/model.ts.
// Ranking by relative request change instead puts a 64Mi sidecar above the
// workload wasting two cores, so the tool and the Rightsizing screen would
// disagree about where the waste is.

type RightsizingClass string

const (
	ClassReduction RightsizingClass = "reduction"
	ClassIncrease  RightsizingClass = "increase"
	ClassReview    RightsizingClass = "review"
	ClassNeedData  RightsizingClass = "need_data"
	ClassInRange   RightsizingClass = "in_range"
)

// Reduction thresholds double as the impact-score denominators, so a CPU and a
// memory change are comparable in one ordering.
const (
	minCPUReduction    = 0.05
	minMemoryReduction = 64 * 1024 * 1024
)

func classRank(class RightsizingClass) int {
	switch class {
	case ClassReduction:
		return 0
	case ClassIncrease:
		return 1
	case ClassReview:
		return 2
	case ClassNeedData:
		return 3
	default:
		return 4
	}
}

// RightsizingImpact is the replica-weighted request change a workload's
// actionable rows add up to. CPU and Memory carry formatted quantities because
// an agent reporting "-2250m" cannot misread the unit the way a bare float can.
type RightsizingImpact struct {
	Replicas int    `json:"replicas"`
	CPU      string `json:"cpu,omitempty"`
	Memory   string `json:"memory,omitempty"`

	CPUChange    float64 `json:"-"`
	MemoryChange float64 `json:"-"`
}

// NeedsManualReview marks a row whose recommendation a human has to weigh:
// autoscaler involvement, OOM history, a limit that conflicts with the request,
// or a reduction against bursty or throttled usage. These rows are excluded
// from impact — counting a reduction nobody should apply unattended as savings
// would rank the workload by waste it cannot actually give back.
func NeedsManualReview(row RightsizingRow) bool {
	if row.HPAManaged || row.CurrentPodOOM || row.WindowOOMEvidence || row.LimitConflict {
		return true
	}
	if IsWithheldRecommendationReason(row.RecommendationReason) {
		return true
	}
	throttled := row.ThrottleRatio != nil && *row.ThrottleRatio >= throttleReviewRatio
	return isReduction(row) && (row.Bursty || throttled)
}

// throttleReviewRatio is the share of throttled CPU periods that makes a row
// worth a human's attention. Shared so the review rule and the row filter
// cannot drift apart on the number.
const throttleReviewRatio = 0.1

// SignificantlyThrottled reports throttling heavy enough that the row should
// reach a reader whatever its fit says. classifyRightsizingFit settles fit from
// the request alone, so a container throttled against a too-low limit reads as
// correctly sized — which is precisely the row worth surfacing.
func SignificantlyThrottled(row RightsizingRow) bool {
	return row.ThrottleAvailable && row.ThrottleRatio != nil && *row.ThrottleRatio >= throttleReviewRatio
}

func isReduction(row RightsizingRow) bool {
	return row.RecommendedRequestValue != nil && row.CurrentRequestValue != nil &&
		*row.RecommendedRequestValue < *row.CurrentRequestValue
}

// ClassifyRows reduces a workload's rows to the one class that decides its
// rank. The order here is precedence, not sort order: missing evidence beats
// every verdict drawn from it, and an under-requested container decides the
// class over an oversized one because under-requesting is the failure that
// takes the workload down. Sorting then leads with reductions — see classRank.
func ClassifyRows(rows []RightsizingRow, replicas int, scaledToZero bool) RightsizingClass {
	if slices.ContainsFunc(rows, RowUnevidenced) {
		return ClassNeedData
	}
	if scaledToZero {
		return ClassReview
	}
	for _, row := range rows {
		if (row.Fit == FitUnderRequested || row.Fit == FitMissingRequest) && row.RecommendedReq != nil {
			return ClassIncrease
		}
	}
	for _, row := range rows {
		if NeedsManualReview(row) {
			return ClassReview
		}
	}
	impact := CalculateImpact(rows, replicas)
	if -impact.CPUChange >= minCPUReduction || -impact.MemoryChange >= minMemoryReduction {
		return ClassReduction
	}
	return ClassInRange
}

func CalculateImpact(rows []RightsizingRow, replicas int) RightsizingImpact {
	count := max(replicas, 0)
	impact := RightsizingImpact{Replicas: count}
	for _, row := range rows {
		if row.RecommendedRequestValue == nil || NeedsManualReview(row) {
			continue
		}
		current := 0.0
		if row.CurrentRequestValue != nil {
			current = *row.CurrentRequestValue
		}
		change := (*row.RecommendedRequestValue - current) * float64(count)
		if row.Resource == "cpu" {
			impact.CPUChange += change
		} else {
			impact.MemoryChange += change
		}
	}
	if impact.CPUChange != 0 {
		impact.CPU = formatSignedRightsizingValue(impact.CPUChange, "cpu")
	}
	if impact.MemoryChange != 0 {
		impact.Memory = formatSignedRightsizingValue(impact.MemoryChange, "memory")
	}
	return impact
}

// ImpactScore normalizes the two resources against their reduction thresholds
// so one ordering can compare a CPU change with a memory change.
func ImpactScore(impact RightsizingImpact) float64 {
	return math.Max(
		math.Abs(impact.CPUChange)/minCPUReduction,
		math.Abs(impact.MemoryChange)/minMemoryReduction,
	)
}

// RightsizingRankLess orders two classified workloads the way the Rightsizing
// screen does: class first, then replica-weighted impact, then identity so the
// order is stable across calls.
func RightsizingRankLess(aClass RightsizingClass, aImpact RightsizingImpact, aKey string,
	bClass RightsizingClass, bImpact RightsizingImpact, bKey string) bool {
	if rankA, rankB := classRank(aClass), classRank(bClass); rankA != rankB {
		return rankA < rankB
	}
	if scoreA, scoreB := ImpactScore(aImpact), ImpactScore(bImpact); scoreA != scoreB {
		return scoreA > scoreB
	}
	return aKey < bKey
}

// formatSignedRightsizingValue always spells CPU in millicores and memory in
// Mi/Gi. formatObservedValue switches CPU to bare cores above 1000m, which
// reads as an unlabelled number once a sign is prepended to it.
func formatSignedRightsizingValue(v float64, resourceName string) string {
	sign := "+"
	if v < 0 {
		sign = "-"
	}
	magnitude := math.Abs(v)
	switch resourceName {
	case "cpu":
		return fmt.Sprintf("%s%.0fm", sign, magnitude*1000)
	case "memory":
		return sign + formatObservedValue(magnitude, "memory")
	}
	return ""
}

// RowUnevidenced reports a row no verdict was drawn from: its usage query
// failed or its history was too short to judge.
func RowUnevidenced(row RightsizingRow) bool {
	return row.QueryError != "" || row.Fit == FitInsufficientHistory
}

// ClassifyWorkload classifies and sizes a workload from the containers that
// actually have evidence. The Rightsizing screen ranks each container as its
// own entry, so one unevidenced sidecar never hides an oversized app container
// there; classifying every row at once would make the whole workload need_data
// and push real savings past the response limit. Impact comes from the same
// containers, so an unjudged container's change never sizes that class.
//
// Unevidenced containers are dropped rather than letting the best container
// win outright: ClassifyRows' precedence is a safety rule — an under-requested
// container decides the class over an oversized one, because under-requesting
// is the failure that takes the workload down — and classRank's reduction-first
// order is a sort rule for the screen. Picking by classRank would let the sort
// rule overturn the safety rule on a workload carrying both.
func ClassifyWorkload(rows []RightsizingRow, replicas int, scaledToZero bool) (RightsizingClass, RightsizingImpact) {
	evidenced, dropped := evidencedContainerRows(rows)
	impact := CalculateImpact(evidenced, replicas)
	// No evidenced rows is no evidence. ClassifyRows would answer in_range,
	// which reads as "correctly sized" on exactly the workloads — init-only or
	// empty pod templates — the response has already declared it cannot judge.
	if len(evidenced) == 0 {
		return ClassNeedData, impact
	}
	class := ClassifyRows(evidenced, replicas, scaledToZero)
	// "Nothing to change" is a verdict the unjudged container never received;
	// only an actionable class from the others stands without it.
	if dropped && class == ClassInRange {
		return ClassNeedData, impact
	}
	return class, impact
}

func evidencedContainerRows(rows []RightsizingRow) ([]RightsizingRow, bool) {
	byContainer := make(map[string][]RightsizingRow, len(rows))
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, seen := byContainer[row.Container]; !seen {
			order = append(order, row.Container)
		}
		byContainer[row.Container] = append(byContainer[row.Container], row)
	}
	evidenced := make([]RightsizingRow, 0, len(rows))
	dropped := false
	for _, container := range order {
		if slices.ContainsFunc(byContainer[container], RowUnevidenced) {
			dropped = true
			continue
		}
		evidenced = append(evidenced, byContainer[container]...)
	}
	return evidenced, dropped
}
