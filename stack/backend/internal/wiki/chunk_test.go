package wiki

import (
	"fmt"
	"strings"
	"testing"
)

// sentence returns a single ~20-token sentence tagged with id so tests can
// assert which sentences landed in which chunk.
func sentence(id int) string {
	return fmt.Sprintf("Sentence %03d carries some weight in this paragraph and runs long enough to matter.", id)
}

// paragraphOf builds a paragraph of n sentences (~20 tokens each).
func paragraphOf(n, firstID int) string {
	parts := make([]string, n)
	for i := range n {
		parts[i] = sentence(firstID + i)
	}
	return strings.Join(parts, " ")
}

func TestChunkEmptyLead(t *testing.T) {
	t.Parallel()
	if got := Chunk("Paris", ""); len(got) != 0 {
		t.Errorf("Chunk(empty) = %q, want none", got)
	}
}

func TestChunkShortLeadIsSingleChunk(t *testing.T) {
	t.Parallel()

	lead := "Paris is the capital of France.\n\nIt is known for the Eiffel Tower."
	got := Chunk("Paris", lead)

	want := []string{"Paris\n\n" + lead}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("Chunk() = %q, want %q", got, want)
	}
}

func TestChunkEveryChunkHasTitlePrefix(t *testing.T) {
	t.Parallel()

	paras := make([]string, 6)
	for i := range paras {
		paras[i] = paragraphOf(10, i*10)
	}
	got := Chunk("Paris", strings.Join(paras, "\n\n"))

	if len(got) < 2 {
		t.Fatalf("Chunk() returned %d chunks, want at least 2", len(got))
	}
	for i, c := range got {
		if !strings.HasPrefix(c, "Paris\n\n") {
			t.Errorf("chunk %d missing title prefix: %q...", i, c[:min(40, len(c))])
		}
	}
}

func TestChunkSplitsOnParagraphBoundaries(t *testing.T) {
	t.Parallel()

	// Two ~200-token paragraphs cannot share one 512-token chunk once the
	// title prefix is counted... they can; three cannot. Use three.
	p1 := paragraphOf(10, 0)
	p2 := paragraphOf(10, 10)
	p3 := paragraphOf(10, 20)
	got := Chunk("Paris", p1+"\n\n"+p2+"\n\n"+p3)

	for i, c := range got {
		body := strings.TrimPrefix(c, "Paris\n\n")
		for _, p := range []string{p1, p2, p3} {
			if strings.Contains(body, p[:40]) && !strings.Contains(body, p) {
				t.Errorf("chunk %d splits a paragraph that fits whole: %q...", i, c[:60])
			}
		}
	}

	var rejoined strings.Builder
	for i, c := range got {
		if i > 0 {
			rejoined.WriteString("\n\n")
		}
		rejoined.WriteString(strings.TrimPrefix(c, "Paris\n\n"))
	}
	wantAll := p1 + "\n\n" + p2 + "\n\n" + p3
	if rejoined.String() != wantAll {
		t.Errorf("chunks lose or reorder content:\ngot  %q\nwant %q", rejoined.String(), wantAll)
	}
}

func TestChunkRespectsTokenBudget(t *testing.T) {
	t.Parallel()

	paras := make([]string, 8)
	for i := range paras {
		paras[i] = paragraphOf(8, i*8)
	}
	got := Chunk("Paris", strings.Join(paras, "\n\n"))

	if len(got) < 2 {
		t.Fatalf("Chunk() returned %d chunks, want at least 2", len(got))
	}
	for i, c := range got {
		if tok := estimateTokens(c); tok > maxChunkTokens {
			t.Errorf("chunk %d estimates %d tokens, over the %d budget", i, tok, maxChunkTokens)
		}
	}
	for i, c := range got[:len(got)-1] {
		if tok := estimateTokens(c); tok < minChunkTokens {
			t.Errorf("non-final chunk %d estimates %d tokens, under the %d floor", i, tok, minChunkTokens)
		}
	}
}

func TestChunkTinyLeadingParagraphMeetsFloor(t *testing.T) {
	t.Parallel()

	// A small first paragraph followed by a near-budget paragraph (each fits
	// the budget alone; together they exceed it) must not flush a sub-floor
	// chunk; the second paragraph tops the first chunk up at sentence
	// boundaries instead.
	tiny := paragraphOf(2, 100)
	big := paragraphOf(23, 0)
	got := Chunk("Paris", tiny+"\n\n"+big)

	if len(got) < 2 {
		t.Fatalf("Chunk() returned %d chunks, want at least 2", len(got))
	}
	for i, c := range got[:len(got)-1] {
		if tok := estimateTokens(c); tok < minChunkTokens {
			t.Errorf("non-final chunk %d estimates %d tokens, under the %d floor", i, tok, minChunkTokens)
		}
	}
	for i, c := range got {
		if tok := estimateTokens(c); tok > maxChunkTokens {
			t.Errorf("chunk %d estimates %d tokens, over the %d budget", i, tok, maxChunkTokens)
		}
	}

	var joined strings.Builder
	for _, c := range got {
		joined.WriteString(strings.TrimPrefix(c, "Paris\n\n"))
		joined.WriteString(" ")
	}
	for i := range 23 {
		if !strings.Contains(joined.String(), sentence(i)) {
			t.Errorf("sentence %d lost during top-up chunking", i)
		}
	}
	if !strings.Contains(joined.String(), tiny) {
		t.Error("first paragraph lost during top-up chunking")
	}
	if !strings.Contains(got[0], tiny+"\n\n"+sentence(0)) {
		t.Errorf("paragraph break lost when topping up: %q", got[0][:min(260, len(got[0]))])
	}
}

func TestChunkUnsplittableParagraph(t *testing.T) {
	t.Parallel()

	// A paragraph with no ". " boundaries (comma-separated list, non-English
	// punctuation) must still respect both the floor and the budget: the
	// top-up falls back to word boundaries.
	words := make([]string, 600)
	for i := range words {
		words[i] = fmt.Sprintf("item%03d,", i)
	}
	lead := "Short opener.\n\n" + strings.Join(words, " ")

	got := Chunk("List", lead)
	if len(got) < 2 {
		t.Fatalf("Chunk() returned %d chunks, want at least 2", len(got))
	}
	for i, c := range got {
		if tok := estimateTokens(c); tok > maxChunkTokens {
			t.Errorf("chunk %d estimates %d tokens, over the %d budget", i, tok, maxChunkTokens)
		}
	}
	for i, c := range got[:len(got)-1] {
		if tok := estimateTokens(c); tok < minChunkTokens {
			t.Errorf("non-final chunk %d estimates %d tokens, under the %d floor", i, tok, minChunkTokens)
		}
	}

	var joined strings.Builder
	for _, c := range got {
		joined.WriteString(strings.TrimPrefix(c, "List\n\n"))
		joined.WriteString(" ")
	}
	for i := range 600 {
		if !strings.Contains(joined.String(), fmt.Sprintf("item%03d,", i)) {
			t.Fatalf("word %d lost during word-boundary chunking", i)
		}
	}
}

func TestChunkOversizedParagraphSplitsOnSentences(t *testing.T) {
	t.Parallel()

	// One giant paragraph (~50 sentences, ~1000 tokens) must split into
	// budget-sized chunks at sentence boundaries.
	para := paragraphOf(50, 0)
	got := Chunk("Paris", para)

	if len(got) < 2 {
		t.Fatalf("Chunk() returned %d chunks for an oversized paragraph, want at least 2", len(got))
	}
	for i, c := range got {
		if tok := estimateTokens(c); tok > maxChunkTokens {
			t.Errorf("chunk %d estimates %d tokens, over the %d budget", i, tok, maxChunkTokens)
		}
		body := strings.TrimPrefix(c, "Paris\n\n")
		if !strings.HasPrefix(body, "Sentence ") {
			t.Errorf("chunk %d does not start on a sentence boundary: %q...", i, body[:min(40, len(body))])
		}
	}

	rejoined := make([]string, 0, len(got))
	for _, c := range got {
		rejoined = append(rejoined, strings.TrimPrefix(c, "Paris\n\n"))
	}
	if strings.Join(rejoined, " ") != para {
		t.Errorf("sentence-split chunks lose content")
	}
}
