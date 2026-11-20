package claimrating

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestLookup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rating string
		want   domain.LiteralVerdict
		ok     bool
	}{
		{"Faux", domain.LiteralInaccurate, true},
		{"FAUX", domain.LiteralInaccurate, true},
		{"Fausse", domain.LiteralInaccurate, true},
		{"Plutôt faux", domain.LiteralInaccurate, true},
		{"Trompeur", domain.LiteralInaccurate, true},
		{"Vrai", domain.LiteralAccurate, true},
		{"Plutôt vrai", domain.LiteralAccurate, true},
		{"Exact", domain.LiteralAccurate, true},
		// Negated / qualified forms must beat the token they contain (longest-first).
		{"Incorrect", domain.LiteralInaccurate, true},
		{"Inexact", domain.LiteralInaccurate, true},
		{"Mostly true", domain.LiteralAccurate, true},
		{"Mostly False", domain.LiteralInaccurate, true},
		{"Half-true", domain.LiteralUnverifiable, true},
		{"Pants on fire", domain.LiteralInaccurate, true},
		{"Mixture", domain.LiteralUnverifiable, true},
		{"Pas de preuve", domain.LiteralUnverifiable, true},
		{"On n'a pas pu vérifier", domain.LiteralUnverifiable, true},
		{"Invérifiable", domain.LiteralUnverifiable, true},
		{"  faux  ", domain.LiteralInaccurate, true},
		// Negation / inversion guard: a debunked claim must never read as accurate.
		{"Untrue", domain.LiteralInaccurate, true},
		{"Not true", domain.LiteralInaccurate, true},
		{"Mostly untrue", domain.LiteralInaccurate, true},
		{"Not accurate", domain.LiteralInaccurate, true},
		// A double-negated ("not fake"/"not false") rating is genuinely ambiguous, so
		// it is rejected (unmapped) rather than guessed either way.
		{"Not fake", "", false},
		{"Not false", "", false},
		// Whole-token matching: "true" must not match inside a larger token.
		{"construe", "", false},
		{"", "", false},
		{"gibberish-rating", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.rating, func(t *testing.T) {
			t.Parallel()
			got, ok := Lookup(tc.rating)
			if ok != tc.ok {
				t.Fatalf("Lookup(%q) ok = %v, want %v", tc.rating, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("Lookup(%q) = %q, want %q", tc.rating, got, tc.want)
			}
		})
	}
}

func TestMapNumeric(t *testing.T) {
	t.Parallel()
	num := func(v, best, worst float64, ws bool) NumericRating {
		return NumericRating{Value: v, ValueSet: true, Best: best, BestSet: true, Worst: worst, WorstSet: ws}
	}
	cases := []struct {
		name string
		n    NumericRating
		want domain.LiteralVerdict
		ok   bool
	}{
		{"low", num(1, 5, 1, true), domain.LiteralInaccurate, true},
		{"high", num(5, 5, 1, true), domain.LiteralAccurate, true},
		{"middle", num(3, 5, 1, true), "", false},
		{"worst defaults to 1", num(1, 5, 0, false), domain.LiteralInaccurate, true},
		{"degenerate", num(1, 1, 1, true), "", false},
		{"no value", NumericRating{BestSet: true, Best: 5}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := MapNumeric(tc.n)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("MapNumeric(%+v) = (%q,%v), want (%q,%v)", tc.n, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestNormalizeFallsBackToUnverifiable(t *testing.T) {
	t.Parallel()
	// Textual wins over numeric.
	if got, ok := Normalize("Faux", NumericRating{Value: 5, ValueSet: true, Best: 5, BestSet: true}); !ok || got != domain.LiteralInaccurate {
		t.Fatalf("Normalize textual-wins = (%q,%v)", got, ok)
	}
	// No textual match, numeric maps.
	if got, ok := Normalize("Non catégorisé", NumericRating{Value: 5, ValueSet: true, Best: 5, BestSet: true, Worst: 1, WorstSet: true}); !ok || got != domain.LiteralAccurate {
		t.Fatalf("Normalize numeric-fallback = (%q,%v)", got, ok)
	}
	// Nothing maps -> unverifiable, mapped=false.
	if got, ok := Normalize("Non catégorisé", NumericRating{}); ok || got != domain.LiteralUnverifiable {
		t.Fatalf("Normalize unmapped = (%q,%v), want (unverifiable,false)", got, ok)
	}
}

func TestFold(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Plutôt Vrai":   "plutot vrai",
		"half-true":     "half true",
		"  MIXTURE  ":   "mixture",
		"pants_on_fire": "pants on fire",
	}
	for in, want := range cases {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}
