package seed_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/seed"
)

const validPoliticalClaim = `[
  {
    "id": "imm-test-1",
    "text": "500 000 immigrés entrent chaque année dont seuls 10% travaillent",
    "literal_verdict": "inaccurate",
    "flags": ["cherry-picked", "missing-context"],
    "source_name": "INSEE",
    "source_url": "https://www.insee.fr/fr/statistiques/8998082",
    "quoted_span": "375 000 entrees en 2022 ; 62,5% en emploi.",
    "outlet": "insee.fr"
  }
]`

func TestLoadPoliticalClaimsValid(t *testing.T) {
	claims, err := seed.LoadPoliticalClaims(strings.NewReader(validPoliticalClaim))
	if err != nil {
		t.Fatalf("LoadPoliticalClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("loaded %d claims, want 1", len(claims))
	}
	c := claims[0]
	if c.ID != "imm-test-1" {
		t.Errorf("id = %q, want imm-test-1", c.ID)
	}
	if c.LiteralVerdict != domain.LiteralInaccurate {
		t.Errorf("literal verdict = %q, want inaccurate", c.LiteralVerdict)
	}
	if len(c.Flags) != 2 || c.Flags[0] != domain.FlagCherryPicked || c.Flags[1] != domain.FlagMissingContext {
		t.Errorf("flags = %v, want [cherry-picked missing-context]", c.Flags)
	}
	if c.SourceURL == "" || c.SourceName == "" || c.Outlet == "" {
		t.Errorf("missing provenance: name=%q url=%q outlet=%q", c.SourceName, c.SourceURL, c.Outlet)
	}
	if c.QuotedSpan == "" {
		t.Error("quoted span (the period-bearing citation) must not be empty")
	}
	// The loader carries no embedding; it is filled at seed time.
	if c.Embedding != nil {
		t.Errorf("embedding must be nil before seeding, got %d dims", len(c.Embedding))
	}
}

// motivatingStatement is the card's driving talking point: it must resolve via
// the curated path with its real source instead of "Invérifiable".
const motivatingStatement = "500 000 immigrés entrent chaque année dont seuls 10% travaillent"

// TestCommittedPoliticalSeedWellFormed proves the committed immigration seed
// loads, carries roughly a dozen entries, and that every entry is well-formed:
// the period-bearing citation (quoted span), a resolvable source URL, and both
// verdict axes are present (a valid literal, and only known manipulation flags).
// It also asserts the motivating statement is curated verbatim so it can match.
func TestCommittedPoliticalSeedWellFormed(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "seed", "political_claims.json"))
	if err != nil {
		t.Fatalf("open committed seed: %v", err)
	}
	defer func() { _ = f.Close() }()

	claims, err := seed.LoadPoliticalClaims(f)
	if err != nil {
		t.Fatalf("LoadPoliticalClaims: %v", err)
	}
	if len(claims) < 10 {
		t.Fatalf("committed seed has %d claims, want roughly a dozen (>=10)", len(claims))
	}

	var hasMotivating bool
	for _, c := range claims {
		if !c.LiteralVerdict.Valid() {
			t.Errorf("claim %q: invalid literal verdict %q", c.ID, c.LiteralVerdict)
		}
		for _, flag := range c.Flags {
			if !flag.Valid() {
				t.Errorf("claim %q: invalid manipulation flag %q", c.ID, flag)
			}
		}
		if !strings.HasPrefix(c.SourceURL, "http") {
			t.Errorf("claim %q: source url %q is not a resolvable URL", c.ID, c.SourceURL)
		}
		if strings.TrimSpace(c.QuotedSpan) == "" {
			t.Errorf("claim %q: empty quoted span (it carries the official figure and period)", c.ID)
		}
		if c.Text == motivatingStatement {
			hasMotivating = true
			if c.LiteralVerdict != domain.LiteralInaccurate {
				t.Errorf("motivating claim literal verdict = %q, want inaccurate", c.LiteralVerdict)
			}
			if len(c.Flags) == 0 {
				t.Error("motivating claim must carry at least one manipulation flag (a true-ish framing used misleadingly)")
			}
		}
	}
	if !hasMotivating {
		t.Errorf("committed seed is missing the motivating statement %q verbatim", motivatingStatement)
	}
}

func TestLoadPoliticalClaimsRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "empty array",
			json: `[]`,
			want: "empty",
		},
		{
			name: "empty id",
			json: `[{"id":"","text":"t","literal_verdict":"accurate","source_name":"n","source_url":"u","outlet":"o"}]`,
			want: "empty id",
		},
		{
			name: "empty text",
			json: `[{"id":"a","text":"","literal_verdict":"accurate","source_name":"n","source_url":"u","outlet":"o"}]`,
			want: "empty text",
		},
		{
			name: "invalid literal verdict",
			json: `[{"id":"a","text":"t","literal_verdict":"bogus","source_name":"n","source_url":"u","outlet":"o"}]`,
			want: "literal verdict",
		},
		{
			name: "invalid flag",
			json: `[{"id":"a","text":"t","literal_verdict":"accurate","flags":["nope"],"source_name":"n","source_url":"u","outlet":"o"}]`,
			want: "manipulation flag",
		},
		{
			name: "empty source url",
			json: `[{"id":"a","text":"t","literal_verdict":"accurate","source_name":"n","source_url":"","outlet":"o"}]`,
			want: "source url",
		},
		{
			name: "empty source name",
			json: `[{"id":"a","text":"t","literal_verdict":"accurate","source_name":"","source_url":"u","outlet":"o"}]`,
			want: "source name",
		},
		{
			name: "empty outlet",
			json: `[{"id":"a","text":"t","literal_verdict":"accurate","source_name":"n","source_url":"u","outlet":""}]`,
			want: "outlet",
		},
		{
			name: "duplicate id",
			json: `[{"id":"a","text":"t","literal_verdict":"accurate","source_name":"n","source_url":"u","outlet":"o"},{"id":"a","text":"t2","literal_verdict":"accurate","source_name":"n","source_url":"u","outlet":"o"}]`,
			want: "duplicate",
		},
		{
			name: "unknown field",
			json: `[{"id":"a","text":"t","literal_verdict":"accurate","source_name":"n","source_url":"u","outlet":"o","bogus":1}]`,
			want: "decode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := seed.LoadPoliticalClaims(strings.NewReader(tt.json))
			if err == nil {
				t.Fatalf("LoadPoliticalClaims(%s) = nil error, want error containing %q", tt.json, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.want)
			}
		})
	}
}
