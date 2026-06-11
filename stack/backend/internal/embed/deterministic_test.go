package embed

import (
	"math"
	"testing"
)

func TestDeterministicStableAndUnit(t *testing.T) {
	t.Parallel()
	const dim = 16
	d := NewDeterministic(dim)
	ctx := t.Context()

	first, err := d.EmbedDocuments(ctx, []string{"common myths"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if len(first) != 1 || len(first[0]) != dim {
		t.Fatalf("got shape %dx?, want 1x%d", len(first), dim)
	}

	// Same text embeds identically across calls.
	second, err := d.EmbedDocuments(ctx, []string{"common myths"})
	if err != nil {
		t.Fatalf("EmbedDocuments (second): %v", err)
	}
	for i := range first[0] {
		if first[0][i] != second[0][i] {
			t.Fatalf("non-deterministic at %d: %v vs %v", i, first[0][i], second[0][i])
		}
	}

	// Unit length, so cosine distance behaves.
	var sum float64
	for _, v := range first[0] {
		sum += float64(v) * float64(v)
	}
	if math.Abs(sum-1) > 1e-4 {
		t.Errorf("vector not unit length: |v|^2 = %v", sum)
	}
}

func TestDeterministicIgnoresInputType(t *testing.T) {
	t.Parallel()
	d := NewDeterministic(16)
	ctx := t.Context()

	doc, err := d.EmbedDocuments(ctx, []string{"the earth is round"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	query, err := d.EmbedQueries(ctx, []string{"the earth is round"})
	if err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	// A query for a stored document's exact text must land on the same vector,
	// so offline similarity search returns the matching fixture.
	for i := range doc[0] {
		if doc[0][i] != query[0][i] {
			t.Fatalf("document and query vectors diverge at %d", i)
		}
	}
}

func TestDeterministicDistinctTextsDiffer(t *testing.T) {
	t.Parallel()
	d := NewDeterministic(64)
	ctx := t.Context()

	out, err := d.EmbedDocuments(ctx, []string{"alpha claim", "beta claim"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	var dot float64
	for i := range out[0] {
		dot += float64(out[0][i]) * float64(out[1][i])
	}
	// Independent pseudo-random unit vectors are near-orthogonal; assert they
	// are clearly not the same direction.
	if dot > 0.5 {
		t.Errorf("distinct texts too similar: cosine = %v", dot)
	}
}

func TestDeterministicNormalizesText(t *testing.T) {
	t.Parallel()
	d := NewDeterministic(16)
	ctx := t.Context()
	a, _ := d.EmbedDocuments(ctx, []string{"hello world"})
	b, _ := d.EmbedDocuments(ctx, []string{"  hello   world  "})
	for i := range a[0] {
		if a[0][i] != b[0][i] {
			t.Fatalf("whitespace changed the deterministic vector at %d", i)
		}
	}
}
