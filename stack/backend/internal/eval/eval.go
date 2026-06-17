// Package eval is the offline golden-eval harness for the retrieve-then-verify
// fact-check path (VER-88). It turns "the verify path feels more accurate" into a
// number: it runs a committed set of labeled statements through both the legacy
// similarity-only path and the grounded retrieve-then-verify path and reports
// per-verdict accuracy, so a regression test can assert the verify path is at
// least as accurate as the recorded baseline before the flag flips on.
//
// The harness is deterministic and self-contained: it depends on no external
// model API and no database. Each golden case carries the evidence passages
// retrieval would surface and a recorded verifier tool-call, so the verify path
// runs the real verify.Client (and its real citation guard) against a fake
// Anthropic server keyed by claim, exercising the actual verdict-mapping wiring
// rather than a stub. This makes the test a regression guard on that wiring and
// on the citation/verdict logic, not on live model quality.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/verovec/truth-in-stream/backend/internal/verify"
)

// Verdict labels. They mirror the verify package's labels so a recorded model
// verdict, an expected label, and a path's output all live in one vocabulary.
const (
	VerdictSupports      = verify.VerdictSupports
	VerdictRefutes       = verify.VerdictRefutes
	VerdictNotEnoughInfo = verify.VerdictNotEnoughInfo
)

// Passage is one retrieved evidence passage for a golden case: the stable
// evidence id a citation round-trips against, the passage text the verifier
// reads, the cosine similarity retrieval reported for it (the legacy baseline
// path's only signal), and Kind, the corpus the hit came from (claim or
// evidence), recorded as fixture provenance so a reviewer can see what retrieval
// surfaced. The verify path uses id and text; the legacy path uses similarity.
type Passage struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Similarity float64 `json:"similarity"`
	Kind       string  `json:"kind"`
}

// ModelVerdict is the recorded verifier tool-call for a golden case: the exact
// structured input the model would return for this claim and these passages. The
// fake Anthropic server replays it so the verify path is deterministic; the real
// citation guard still runs over it, so a recorded verdict whose citation does
// not ground is downgraded by the guard exactly as in production.
type ModelVerdict struct {
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence"`
	Citations  []struct {
		EvidenceID string `json:"evidence_id"`
		QuotedSpan string `json:"quoted_span"`
	} `json:"citations"`
	Rationale string `json:"rationale"`
}

// Case is one labeled golden statement: the claim, the verdict the evidence
// actually warrants (Expected), a provenance note justifying that label, the
// retrieved passages, and the recorded model verdict. Adversarial marks the
// same-topic-opposite-truth cases that expose the legacy similarity bug.
type Case struct {
	ID           string       `json:"id"`
	Statement    string       `json:"statement"`
	Expected     string       `json:"expected"`
	Provenance   string       `json:"provenance"`
	Adversarial  bool         `json:"adversarial"`
	Passages     []Passage    `json:"passages"`
	ModelVerdict ModelVerdict `json:"model_verdict"`
}

// Golden is the committed eval set: a free-text note and the labeled cases.
type Golden struct {
	About string `json:"_about"`
	Cases []Case `json:"cases"`
}

// LoadGolden reads and validates the committed golden set. It fails on a missing
// file, malformed JSON, an empty set, a duplicate id, an unknown expected label,
// or a recorded verdict carrying an unknown label, so a fixture authoring slip is
// a hard error rather than a silently skewed accuracy number.
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
		if !validLabel(c.Expected) {
			return Golden{}, fmt.Errorf("eval: case %q has unknown expected verdict %q", c.ID, c.Expected)
		}
		if !validLabel(c.ModelVerdict.Verdict) {
			return Golden{}, fmt.Errorf("eval: case %q recorded verdict has unknown label %q", c.ID, c.ModelVerdict.Verdict)
		}
	}
	return g, nil
}

// validLabel reports whether v is one of the three first-class verdict labels.
func validLabel(v string) bool {
	return v == VerdictSupports || v == VerdictRefutes || v == VerdictNotEnoughInfo
}

// LegacyVerdict is the legacy similarity-only path's verdict for one case,
// modeled on the inputs the old path actually had: ranked matches and their
// similarities, with no entailment step. The old path surfaced the strongest
// match above a corroboration floor as support and otherwise reported nothing
// settled it. It cannot tell a passage that affirms the claim from one that
// refutes it or merely shares its topic - that is the "similarity is not
// entailment" bug this eval exists to measure - so a strong topical hit always
// reads as supports. With no passage clearing the floor it returns
// not_enough_info. legacyFloor is the corroboration similarity floor.
func LegacyVerdict(c Case, legacyFloor float64) string {
	best := -1.0
	for _, p := range c.Passages {
		if p.Similarity > best {
			best = p.Similarity
		}
	}
	if best >= legacyFloor {
		return VerdictSupports
	}
	return VerdictNotEnoughInfo
}

// VerifyVerdict is the retrieve-then-verify path's verdict for one case. It
// mirrors the live path's verifyClaim wiring: with no passages it returns
// not_enough_info without a model call (a verdict without evidence is
// meaningless); otherwise it calls the real verify.Client, which forces the
// recorded tool call through the fake server and runs the real citation guard, so
// the label that comes back is exactly the one the production path would emit for
// these passages and this recorded model output.
func VerifyVerdict(ctx context.Context, v *verify.Client, c Case) (string, error) {
	if len(c.Passages) == 0 {
		return VerdictNotEnoughInfo, nil
	}
	passages := make([]verify.Passage, 0, len(c.Passages))
	for _, p := range c.Passages {
		passages = append(passages, verify.Passage{ID: p.ID, Text: p.Text})
	}
	res, err := v.Verify(ctx, c.Statement, passages)
	if err != nil {
		return "", fmt.Errorf("eval: verify case %q: %w", c.ID, err)
	}
	return res.Verdict, nil
}

// Report is one path's accuracy over the golden set: the overall correct count
// and a per-label breakdown keyed by the expected label, so a regression can see
// not just the headline number but which verdict class moved.
type Report struct {
	Total      int
	Correct    int
	ByExpected map[string]LabelStat
}

// LabelStat is the accuracy for one expected label: how many cases carried it and
// how many the path got right.
type LabelStat struct {
	Total   int
	Correct int
}

// Accuracy is the overall fraction correct in [0, 1]; an empty report is 0.
func (r Report) Accuracy() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(r.Correct) / float64(r.Total)
}

// score tallies a verdict against the expected label into a report, allocating
// the per-label map on first use.
func (r *Report) score(expected, got string) {
	if r.ByExpected == nil {
		r.ByExpected = make(map[string]LabelStat)
	}
	r.Total++
	stat := r.ByExpected[expected]
	stat.Total++
	if got == expected {
		r.Correct++
		stat.Correct++
	}
	r.ByExpected[expected] = stat
}

// RunLegacy scores the legacy similarity-only path over the whole set.
func RunLegacy(g Golden, legacyFloor float64) Report {
	var r Report
	for _, c := range g.Cases {
		r.score(c.Expected, LegacyVerdict(c, legacyFloor))
	}
	return r
}

// RunVerify scores the retrieve-then-verify path over the whole set, driving the
// real verify.Client. It returns the first verify error so a transport or wiring
// regression fails the eval loudly rather than counting as a wrong answer.
func RunVerify(ctx context.Context, v *verify.Client, g Golden) (Report, error) {
	var r Report
	for _, c := range g.Cases {
		got, err := VerifyVerdict(ctx, v, c)
		if err != nil {
			return Report{}, err
		}
		r.score(c.Expected, got)
	}
	return r, nil
}

// Format renders a report as a short, stable multi-line summary for test logs:
// the overall accuracy then each label in sorted order, so two runs diff cleanly.
func (r Report) Format(name string) string {
	out := fmt.Sprintf("%s: %d/%d correct (%.1f%%)", name, r.Correct, r.Total, r.Accuracy()*100)
	labels := make([]string, 0, len(r.ByExpected))
	for label := range r.ByExpected {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		s := r.ByExpected[label]
		out += fmt.Sprintf("\n  %-16s %d/%d", label, s.Correct, s.Total)
	}
	return out
}
