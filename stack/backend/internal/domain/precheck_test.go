package domain

import "testing"

func TestSkipReasonValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		reason SkipReason
		want   bool
	}{
		{"checked (none)", SkipReasonNone, true},
		{"not a claim", SkipReasonNotAClaim, true},
		{"not covered", SkipReasonNotCovered, true},
		{"unknown", SkipReason("bogus"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.reason.Valid(); got != tc.want {
				t.Errorf("SkipReason(%q).Valid() = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}

func TestSkipReasonDisjointFromVerdict(t *testing.T) {
	t.Parallel()
	// A skip reason must never collide with a verdict value, or a skipped
	// segment could be read back as a fact-check outcome.
	for _, r := range []SkipReason{SkipReasonNotAClaim, SkipReasonNotCovered} {
		if Verdict(r).Valid() {
			t.Errorf("skip reason %q overlaps a valid verdict", r)
		}
	}
}

func TestPrecheckDecisionConstructors(t *testing.T) {
	t.Parallel()
	if got := Checkable(); !got.Checkable || got.Reason != SkipReasonNone {
		t.Errorf("Checkable() = %+v, want checkable with no reason", got)
	}
	got := Skipped(SkipReasonNotCovered)
	if got.Checkable || got.Reason != SkipReasonNotCovered {
		t.Errorf("Skipped(NotCovered) = %+v, want not checkable with reason not_covered", got)
	}
}
