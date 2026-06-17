package source

import (
	"strings"
	"testing"
)

func TestEvidenceIDRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   EvidenceID
	}{
		{"insee series", NewEvidenceID(KindStatsINSEE, "001688370", 0)},
		{"eurostat dataset", NewEvidenceID(KindStatsEurostat, "une_rt_a", 3)},
		{"websearch host", NewEvidenceID(KindWebSearch, "www.insee.fr", 2)},
		{"zero index", NewEvidenceID(KindWebSearch, "example.org", 0)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			encoded := tc.id.String()
			got, err := ParseEvidenceID(encoded)
			if err != nil {
				t.Fatalf("ParseEvidenceID(%q): %v", encoded, err)
			}
			if got != tc.id {
				t.Fatalf("round trip: got %+v, want %+v (encoded %q)", got, tc.id, encoded)
			}
			if got.String() != encoded {
				t.Fatalf("re-encode: got %q, want %q", got.String(), encoded)
			}
		})
	}
}

func TestNewEvidenceIDSanitisesSeparator(t *testing.T) {
	t.Parallel()

	id := NewEvidenceID(KindWebSearch, "weird:source:id", 1)
	if id.SourceID != "weird_source_id" {
		t.Fatalf("source id not sanitized: got %q", id.SourceID)
	}
	encoded := id.String()
	got, err := ParseEvidenceID(encoded)
	if err != nil {
		t.Fatalf("ParseEvidenceID(%q): %v", encoded, err)
	}
	if got != id {
		t.Fatalf("round trip after sanitize: got %+v, want %+v", got, id)
	}
}

func TestParseEvidenceIDRecoversSeparatorInSourceID(t *testing.T) {
	t.Parallel()

	// An id constructed directly (not via NewEvidenceID) may carry a separator
	// in its source id, e.g. a host:port. ParseEvidenceID must recover the whole
	// source id rather than reject it or truncate it.
	id := EvidenceID{Kind: KindWebSearch, SourceID: "news.example.com:8080", Index: 4}
	got, err := ParseEvidenceID(id.String())
	if err != nil {
		t.Fatalf("ParseEvidenceID(%q): %v", id.String(), err)
	}
	if got != id {
		t.Fatalf("round trip with separator in source id: got %+v, want %+v", got, id)
	}
}

func TestParseEvidenceIDKeepsInnerSeparators(t *testing.T) {
	t.Parallel()

	got, err := ParseEvidenceID("insee:a:b:0")
	if err != nil {
		t.Fatalf("ParseEvidenceID: %v", err)
	}
	want := EvidenceID{Kind: KindStatsINSEE, SourceID: "a:b", Index: 0}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestKindConstantsAreSeparatorFree(t *testing.T) {
	t.Parallel()

	// The encoding takes the kind up to the first separator, so a kind constant
	// must never contain the separator or every id minted with it fails to
	// parse. Guard the invariant at the type's whole constant set.
	for _, k := range []Kind{KindStats, KindStatsINSEE, KindStatsEurostat, KindWebSearch} {
		if strings.Contains(string(k), idSeparator) {
			t.Errorf("kind %q contains the id separator %q", k, idSeparator)
		}
	}
}

func TestNewEvidenceIDClampsNegativeIndex(t *testing.T) {
	t.Parallel()

	if got := NewEvidenceID(KindStatsINSEE, "x", -5).Index; got != 0 {
		t.Fatalf("negative index not clamped: got %d", got)
	}
}

func TestParseEvidenceIDRejectsMalformed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"no separator", "insee001688370"},
		{"too few parts", "insee:001688370"},
		{"non-numeric index", "insee:001688370:abc"},
		{"negative index", "insee:001688370:-1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseEvidenceID(tc.in); err == nil {
				t.Fatalf("ParseEvidenceID(%q): want error, got nil", tc.in)
			}
		})
	}
}

func TestQueryHint(t *testing.T) {
	t.Parallel()

	q := Query{Text: "le chomage", Hints: map[string]string{"idbank": "001688370"}}
	if v, ok := q.Hint("idbank"); !ok || v != "001688370" {
		t.Fatalf("Hint(idbank): got %q, %v", v, ok)
	}
	if _, ok := q.Hint("missing"); ok {
		t.Fatalf("Hint(missing): want absent")
	}
	var empty Query
	if _, ok := empty.Hint("anything"); ok {
		t.Fatalf("Hint on nil map: want absent")
	}
}
