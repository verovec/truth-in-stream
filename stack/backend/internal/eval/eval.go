// Package eval is the offline golden-eval harness for the French political
// two-axis fact-check path (FACTCHECK_POLITICAL). It turns "the political verify
// path feels right" into a number: it runs a committed set of labeled French
// political claims through the grounded two-axis verifier and reports per-literal
// accuracy and flag accuracy, so a regression test can assert the path stays at
// least as accurate as the recorded baseline before the flag flips on.
//
// The harness is deterministic and self-contained: it depends on no external
// model API and no database. Each golden case carries the evidence retrieval
// would surface and a recorded two-axis verifier tool-call, so the political
// path runs the real verify.Client (VerifyPolitical, with its real citation and
// flag guard) against a fake Anthropic server keyed by claim, exercising the
// actual literal-verdict + flag wiring rather than a stub. This makes the test a
// regression guard on that wiring and on the citation/flag logic, not on live
// model quality.
//
// The production default is NOT flipped here. The offline gate measures the
// wiring; flipping FACTCHECK_POLITICAL on the strength of a faked run alone would
// be dishonest, because it does not measure whether live retrieval plus the live
// Claude verifier hit the baseline on a real corpus. The real-model eval and
// flag-flip procedure are documented in PROCEDURE.md; the default stays off until
// that bar is met.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/llm"
	"github.com/verovec/truth-in-stream/backend/internal/verify"
)

// Target selects the LLM backend a golden run scores under, so the same French
// political golden set can be run against DeepSeek (the default), Anthropic
// (Claude Haiku), or Gemini for an apples-to-apples literal/flag accuracy
// comparison. Provider is the validated llm.ProviderName (empty defaults to
// DeepSeek, matching llm.NewClient); Model is the per-run model override (empty
// falls back to the provider's default). Keys are not held here: they come from
// the environment in the real-model run and from a fake server in the offline
// gate, so a Target never carries a secret. The offline CI gate builds a Target
// per provider and points the verifier at a per-provider fake; the operator's
// real-model run builds the same Target from LLM_PROVIDER and the provider key
// (see PROCEDURE.md).
type Target struct {
	Provider llm.ProviderName
	Model    string
}

// VerifierConfig renders the target into the verify.Config the political path's
// verify.New consumes, threading the per-provider key supplied by the caller
// (the live key from the environment in a real-model run, a fake key in the
// offline gate) onto the field the selected provider reads. A Gemini target keys
// on GeminiAPIKey, an Anthropic target on APIKey, and a DeepSeek target (the
// default when Provider is empty) on DeepSeekAPIKey - the same split the rest of
// the stack uses, so the eval targets a provider exactly as production would.
func (t Target) VerifierConfig(apiKey string) verify.Config {
	cfg := verify.Config{Provider: t.Provider, Model: t.Model}
	switch t.Provider {
	case llm.ProviderGemini:
		cfg.GeminiAPIKey = apiKey
	case llm.ProviderAnthropic:
		cfg.APIKey = apiKey
	default:
		// Empty provider defaults to DeepSeek, matching llm.NewClient.
		cfg.DeepSeekAPIKey = apiKey
	}
	return cfg
}

// Literal verdict labels. They mirror the verify package's political literal
// labels so a recorded model verdict, an expected label, and the path's output
// all live in one vocabulary.
const (
	LiteralAccurate     = verify.LiteralAccurate
	LiteralInaccurate   = verify.LiteralInaccurate
	LiteralUnverifiable = verify.LiteralUnverifiable
)

// Manipulation flags. They mirror the verify package's closed flag vocabulary so
// an expected flag set and a recorded model flag set cannot drift from the labels
// the guard accepts.
const (
	FlagMissingContext      = verify.FlagMissingContext
	FlagCherryPicked        = verify.FlagCherryPicked
	FlagOutdated            = verify.FlagOutdated
	FlagMisattributed       = verify.FlagMisattributed
	FlagMisleadingCausation = verify.FlagMisleadingCausation
)

// Passage is one retrieved evidence passage for a golden case: the stable
// evidence id a citation round-trips against, the passage text the verifier
// reads, and Kind, the corpus the hit came from (claim or evidence), recorded as
// fixture provenance so a reviewer can see what retrieval surfaced.
type Passage struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Kind string `json:"kind"`
}

// Citation is one recorded grounding the model returned.
type Citation struct {
	EvidenceID string `json:"evidence_id"`
	QuotedSpan string `json:"quoted_span"`
}

// ModelVerdict is the recorded two-axis verifier tool-call for a golden case: the
// exact structured input the model would return for this claim and these
// passages. The fake Anthropic server replays it so the political path is
// deterministic; the real citation and flag guard still runs over it, so a
// recorded verdict whose citation does not ground, or whose flag is out of
// vocabulary, is corrected by the guard exactly as in production.
type ModelVerdict struct {
	Literal    string     `json:"literal"`
	Basis      string     `json:"basis"`
	Flags      []string   `json:"flags"`
	Confidence float64    `json:"confidence"`
	Citations  []Citation `json:"citations"`
	Rationale  string     `json:"rationale"`
}

// Case is one labeled French political golden statement: the claim, the literal
// verdict the evidence actually warrants (ExpectedLiteral), the manipulation
// flags the framing actually warrants (ExpectedFlags), the claim type the case
// exercises, a provenance note justifying the labels, the retrieved passages, and
// the recorded model verdict. Adversarial marks the true-but-misleading and
// same-topic-opposite-truth cases that are the redesign's whole point.
type Case struct {
	ID              string       `json:"id"`
	Statement       string       `json:"statement"`
	ClaimType       string       `json:"claim_type"`
	ExpectedLiteral string       `json:"expected_literal"`
	ExpectedFlags   []string     `json:"expected_flags"`
	Provenance      string       `json:"provenance"`
	Adversarial     bool         `json:"adversarial"`
	Passages        []Passage    `json:"passages"`
	ModelVerdict    ModelVerdict `json:"model_verdict"`
}

// Golden is the committed eval set: a free-text note and the labeled cases.
type Golden struct {
	About string `json:"_about"`
	Cases []Case `json:"cases"`
}

// LoadGolden reads and validates the committed golden set. It fails on a missing
// file, malformed JSON, an empty set, a duplicate id, an unknown expected literal
// label, an unknown expected flag, an unknown recorded literal label, or an
// unknown recorded flag, so a fixture authoring slip is a hard error rather than a
// silently skewed accuracy number.
func LoadGolden(path string) (Golden, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Golden{}, fmt.Errorf("eval: read golden set: %w", err)
	}
	var g Golden
	if err := json.Unmarshal(raw, &g); err != nil {
		return Golden{}, fmt.Errorf("eval: decode golden set: %w", err)
	}
	if len(g.Cases) == 0 {
		return Golden{}, fmt.Errorf("eval: golden set is empty")
	}
	seen := make(map[string]struct{}, len(g.Cases))
	for _, c := range g.Cases {
		if c.ID == "" {
			return Golden{}, fmt.Errorf("eval: a case is missing its id")
		}
		if _, dup := seen[c.ID]; dup {
			return Golden{}, fmt.Errorf("eval: duplicate case id %q", c.ID)
		}
		seen[c.ID] = struct{}{}
		if !validLiteral(c.ExpectedLiteral) {
			return Golden{}, fmt.Errorf("eval: case %q has unknown expected literal %q", c.ID, c.ExpectedLiteral)
		}
		if !validLiteral(c.ModelVerdict.Literal) {
			return Golden{}, fmt.Errorf("eval: case %q recorded verdict has unknown literal %q", c.ID, c.ModelVerdict.Literal)
		}
		if err := validateFlags(c.ID, "expected", c.ExpectedFlags); err != nil {
			return Golden{}, err
		}
		if err := validateFlags(c.ID, "recorded", c.ModelVerdict.Flags); err != nil {
			return Golden{}, err
		}
		if err := validateRecordedCitations(c); err != nil {
			return Golden{}, err
		}
	}
	return g, nil
}

// validateRecordedCitations rejects a recorded verdict whose citations could not
// survive the production citation guard, so an authoring slip is a hard load error
// rather than a fixture that silently exercises the wrong code path at the gate. It
// enforces that every recorded citation's evidence_id resolves to a passage in the
// same case and its quoted_span is a non-empty substring of that passage, and that
// an unverifiable verdict carries basis knowledge with no citations (the state the
// guard forces) so the recorded fixture matches what production would emit.
func validateRecordedCitations(c Case) error {
	if c.ModelVerdict.Literal == LiteralUnverifiable {
		if c.ModelVerdict.Basis != verify.BasisKnowledge || len(c.ModelVerdict.Citations) != 0 {
			return fmt.Errorf("eval: case %q records an unverifiable verdict that is not basis %q with no citations", c.ID, verify.BasisKnowledge)
		}
		return nil
	}
	byID := make(map[string]string, len(c.Passages))
	for _, p := range c.Passages {
		byID[p.ID] = p.Text
	}
	for _, cit := range c.ModelVerdict.Citations {
		text, ok := byID[cit.EvidenceID]
		if !ok {
			return fmt.Errorf("eval: case %q cites unknown evidence_id %q", c.ID, cit.EvidenceID)
		}
		if cit.QuotedSpan == "" || !strings.Contains(text, cit.QuotedSpan) {
			return fmt.Errorf("eval: case %q citation span %q is not a substring of passage %q", c.ID, cit.QuotedSpan, cit.EvidenceID)
		}
	}
	return nil
}

// validLiteral reports whether v is one of the three first-class literal labels.
func validLiteral(v string) bool {
	return v == LiteralAccurate || v == LiteralInaccurate || v == LiteralUnverifiable
}

// knownFlags is the closed manipulation-flag vocabulary, mirrored from the verify
// package so an authoring slip in the golden set is caught at load time.
var knownFlags = map[string]struct{}{
	FlagMissingContext:      {},
	FlagCherryPicked:        {},
	FlagOutdated:            {},
	FlagMisattributed:       {},
	FlagMisleadingCausation: {},
}

// validateFlags reports an error if any flag is outside the closed vocabulary.
func validateFlags(caseID, which string, flags []string) error {
	for _, f := range flags {
		if _, ok := knownFlags[f]; !ok {
			return fmt.Errorf("eval: case %q %s flags carry unknown flag %q", caseID, which, f)
		}
	}
	return nil
}

// Outcome is the political path's two-axis judgment of one case, reduced to the
// two graded axes: the literal verdict and the (sorted) set of surviving
// manipulation flags. It is what the harness scores against the case's expected
// labels.
type Outcome struct {
	Literal string
	Flags   []string
}

// PoliticalVerdict is the political verify path's outcome for one case. It mirrors
// the live path's wiring: with no passages there is nothing to check against, so
// it returns an unverifiable, unflagged outcome without a model call; otherwise it
// calls the real verify.Client (VerifyPolitical), which forces the recorded tool
// call through the fake server and runs the real citation and flag guard, so the
// literal verdict and flags that come back are exactly the ones the production
// path would emit for these passages and this recorded model output.
func PoliticalVerdict(ctx context.Context, v *verify.Client, c Case) (Outcome, error) {
	if len(c.Passages) == 0 {
		return Outcome{Literal: LiteralUnverifiable}, nil
	}
	passages := make([]verify.Passage, 0, len(c.Passages))
	for _, p := range c.Passages {
		passages = append(passages, verify.Passage{ID: p.ID, Text: p.Text})
	}
	res, err := v.VerifyPolitical(ctx, c.Statement, passages)
	if err != nil {
		return Outcome{}, fmt.Errorf("eval: verify case %q: %w", c.ID, err)
	}
	return Outcome{Literal: res.Literal, Flags: sortedFlags(res.Flags)}, nil
}

// Report is the political path's accuracy over the golden set on both axes:
// overall literal-correct and flag-correct counts plus a per-literal-label
// breakdown, so a regression can see not just the headline numbers but which
// verdict class moved. A case counts as flag-correct only when its surviving flag
// set exactly equals its expected flag set, so a dropped or spurious flag is a
// miss.
type Report struct {
	Total          int
	LiteralCorrect int
	FlagCorrect    int
	ByLiteral      map[string]LabelStat
}

// LabelStat is the accuracy for one expected literal label: how many cases
// carried it and how many the path got right on the literal axis.
type LabelStat struct {
	Total   int
	Correct int
}

// LiteralAccuracy is the overall fraction of cases whose literal verdict matched,
// in [0, 1]; an empty report is 0.
func (r Report) LiteralAccuracy() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(r.LiteralCorrect) / float64(r.Total)
}

// FlagAccuracy is the overall fraction of cases whose surviving flag set exactly
// matched the expected flag set, in [0, 1]; an empty report is 0.
func (r Report) FlagAccuracy() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(r.FlagCorrect) / float64(r.Total)
}

// score tallies one case's outcome against its expected labels into the report,
// allocating the per-label map on first use.
func (r *Report) score(c Case, got Outcome) {
	if r.ByLiteral == nil {
		r.ByLiteral = make(map[string]LabelStat)
	}
	r.Total++
	stat := r.ByLiteral[c.ExpectedLiteral]
	stat.Total++
	if got.Literal == c.ExpectedLiteral {
		r.LiteralCorrect++
		stat.Correct++
	}
	r.ByLiteral[c.ExpectedLiteral] = stat
	if slices.Equal(got.Flags, sortedFlags(c.ExpectedFlags)) {
		r.FlagCorrect++
	}
}

// RunPolitical scores the two-axis political path over the whole set, driving the
// real verify.Client. It returns the first verify error so a transport or wiring
// regression fails the eval loudly rather than counting as a wrong answer.
func RunPolitical(ctx context.Context, v *verify.Client, g Golden) (Report, error) {
	var r Report
	for _, c := range g.Cases {
		got, err := PoliticalVerdict(ctx, v, c)
		if err != nil {
			return Report{}, err
		}
		r.score(c, got)
	}
	return r, nil
}

// sortedFlags returns a sorted copy of the flags so two flag sets can be compared
// order-independently. A nil or empty input yields nil, matching the verify
// guard's honest-framing representation.
func sortedFlags(flags []string) []string {
	if len(flags) == 0 {
		return nil
	}
	out := slices.Clone(flags)
	slices.Sort(out)
	return out
}

// Format renders a report as a short, stable multi-line summary for test logs:
// the overall literal and flag accuracy, then each literal label in sorted order,
// so two runs diff cleanly.
func (r Report) Format(name string) string {
	out := fmt.Sprintf("%s: literal %d/%d (%.1f%%), flags %d/%d (%.1f%%)",
		name, r.LiteralCorrect, r.Total, r.LiteralAccuracy()*100,
		r.FlagCorrect, r.Total, r.FlagAccuracy()*100)
	labels := make([]string, 0, len(r.ByLiteral))
	for label := range r.ByLiteral {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		s := r.ByLiteral[label]
		out += fmt.Sprintf("\n  %-14s %d/%d", label, s.Correct, s.Total)
	}
	return out
}
