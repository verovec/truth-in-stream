package service

import "testing"

// TestSpeakerTally drives the pure per-speaker tally through the behaviors it
// pins down: an empty tally is all-zero, each verdict state moves only its own
// count, unverifiable is tallied exactly like the other two, counts accumulate
// across claims, and an unknown state is ignored.
func TestSpeakerTally(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		observe          []string
		wantCredible     int
		wantDisputed     int
		wantUnverifiable int
	}{
		{
			name: "no claims is all zero",
		},
		{
			name:         "one credible moves only the credible count",
			observe:      []string{VerdictCredible},
			wantCredible: 1,
		},
		{
			name:         "one disputed moves only the disputed count",
			observe:      []string{VerdictDisputed},
			wantDisputed: 1,
		},
		{
			name:             "unverifiable is tallied like the others",
			observe:          []string{VerdictUnverifiable, VerdictUnverifiable},
			wantUnverifiable: 2,
		},
		{
			name:             "mixed verdicts each accumulate their own count",
			observe:          []string{VerdictCredible, VerdictCredible, VerdictDisputed, VerdictUnverifiable},
			wantCredible:     2,
			wantDisputed:     1,
			wantUnverifiable: 1,
		},
		{
			name:         "unknown state is ignored entirely",
			observe:      []string{"not_a_verdict", VerdictCredible},
			wantCredible: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := &speakerCredibility{}
			last := sc.snapshot()
			for _, state := range tc.observe {
				last = sc.observe(state)
			}
			if last.Credible != tc.wantCredible {
				t.Errorf("credible = %d, want %d", last.Credible, tc.wantCredible)
			}
			if last.Disputed != tc.wantDisputed {
				t.Errorf("disputed = %d, want %d", last.Disputed, tc.wantDisputed)
			}
			if last.Unverifiable != tc.wantUnverifiable {
				t.Errorf("unverifiable = %d, want %d", last.Unverifiable, tc.wantUnverifiable)
			}
		})
	}
}

// TestSpeakerTallyFramingIsOrthogonal asserts the political path's second axis: a
// manipulation flag bumps the misleading-framing tally without moving any
// credibility count, and a flagged-but-accurate claim moves the credible count
// (via observe) and the framing tally (via observeFraming) independently.
func TestSpeakerTallyFramingIsOrthogonal(t *testing.T) {
	t.Parallel()

	t.Run("flag alone moves only the framing tally", func(t *testing.T) {
		t.Parallel()
		sc := &speakerCredibility{}
		got := sc.observeFraming()
		if got.Credible != 0 || got.Disputed != 0 || got.Unverifiable != 0 {
			t.Errorf("credibility tallies = %d/%d/%d, want all zero", got.Credible, got.Disputed, got.Unverifiable)
		}
		if got.MisleadingFraming != 1 {
			t.Errorf("misleading framing = %d, want 1", got.MisleadingFraming)
		}
	})

	t.Run("accurate-but-flagged moves the credible count and the framing tally", func(t *testing.T) {
		t.Parallel()
		sc := &speakerCredibility{}
		sc.observe(VerdictCredible)
		got := sc.observeFraming()
		if got.Credible != 1 {
			t.Errorf("credible = %d, want 1", got.Credible)
		}
		if got.MisleadingFraming != 1 {
			t.Errorf("misleading framing = %d, want 1", got.MisleadingFraming)
		}
	})

	t.Run("framing tally accumulates across claims", func(t *testing.T) {
		t.Parallel()
		sc := &speakerCredibility{}
		sc.observeFraming()
		got := sc.observeFraming()
		if got.MisleadingFraming != 2 {
			t.Errorf("misleading framing = %d, want 2", got.MisleadingFraming)
		}
	})
}
