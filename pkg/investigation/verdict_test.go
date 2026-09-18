package investigation

import (
	"strings"
	"testing"
)

// The flag alone can never retire the assessment a reader is looking at: a
// revision must carry a headline and a verdict, or the turn is an answer.
func TestSettleRevisionKeepsOnlyCompleteVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    Verdict
		want bool
	}{
		{"complete", Verdict{RevisesAssessment: true, Summary: "It changed.", RootCause: "x"}, true},
		{"healthy", Verdict{RevisesAssessment: true, Summary: "Fine now.", Healthy: true}, true},
		{"no summary", Verdict{RevisesAssessment: true, RootCause: "x"}, false},
		{"no verdict", Verdict{RevisesAssessment: true, Summary: "Words."}, false},
	} {
		v := tc.v
		demoted := v.SettleRevision()
		if v.RevisesAssessment != tc.want || demoted == tc.want {
			t.Errorf("%s: revisesAssessment = %v (demoted %v), want %v", tc.name, v.RevisesAssessment, demoted, tc.want)
		}
	}
}

func TestAssessesFollowsTheTurnRule(t *testing.T) {
	answer := Verdict{RootCause: "x"}
	revision := Verdict{RootCause: "x", RevisesAssessment: true}
	for _, tc := range []struct {
		name string
		turn Turn
		v    Verdict
		want bool
	}{
		{"initial", Turn{}, answer, true},
		{"verification", Turn{Question: "Recheck", Verify: true}, answer, true},
		{"ordinary question", Turn{Question: "Why?"}, answer, false},
		{"revising question", Turn{Question: "Why?"}, revision, true},
		{"explanation", Turn{Explanation: true}, answer, false},
		{"apply", Turn{Apply: true}, answer, false},
	} {
		if got := tc.v.Assesses(tc.turn); got != tc.want {
			t.Errorf("%s: assesses = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPreviewAndExplanationShape(t *testing.T) {
	if got := (Verdict{Summary: "Headline.", RootCause: "cause"}).Preview(); got != "Headline." {
		t.Fatalf("a complete verdict previews its headline, got %q", got)
	}
	if got := (Verdict{Summary: "Words only."}).Preview(); got != "" {
		t.Fatalf("a summary without a verdict is an answer, not a preview: %q", got)
	}
	if got := (Verdict{RootCause: "cause"}).Preview(); got != "cause" {
		t.Fatalf("an older verdict previews its cause, got %q", got)
	}
	if got := (Verdict{Healthy: true}).Preview(); got != "Healthy" {
		t.Fatalf("healthy preview = %q", got)
	}
	explained := Verdict{RootCause: "Simplified cause", Healthy: true, Report: "Plain words.", Remediation: []string{"fix"}}.AsExplanation()
	if explained.Report != "Plain words." || explained.RootCause != "" || explained.Healthy || explained.Remediation != nil {
		t.Fatalf("an explanation keeps only its prose: %+v", explained)
	}
	if !(Verdict{Inconclusive: true}).Structured() || (Verdict{Report: strings.Repeat("x", 10)}).Structured() {
		t.Fatal("structured means a conclusion, not prose")
	}
}
