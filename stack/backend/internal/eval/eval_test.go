package eval

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/llm"

	"github.com/verovec/truth-in-stream/backend/internal/verify"
)

// goldenPath is the committed French political eval set the harness and the gate
// run over.
const goldenPath = "testdata/golden.json"

// politicalToolName is the tool the two-axis verifier forces; the fake server must
// answer under this name or the shared llm transport rejects the response. It
// mirrors the verify package's unexported politicalToolName constant.
const politicalToolName = "record_assessment"

// messagesRequest is the slice of the Anthropic Messages request body the fake
// server reads: just the user message text blocks, enough to recover the claim.
type messagesRequest struct {
	Messages []struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
}

// recordedModelServer is a fake Anthropic Messages server that replays each golden
// case's recorded two-axis verifier tool call. It maps an incoming request to a
// case by finding the case whose statement appears as the "Claim:" line in the
// user message (verify.buildPrompt renders it there, shared by the political
// path), so the political path runs end to end against deterministic, per-claim
// model output with no network.
func recordedModelServer(t *testing.T, g Golden) *httptest.Server {
	t.Helper()
	byStatement := make(map[string]Case, len(g.Cases))
	for _, c := range g.Cases {
		byStatement[c.Statement] = c
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		var req messagesRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "decode", http.StatusBadRequest)
			return
		}
		user := userText(req)
		match, ok := matchCase(user, byStatement)
		if !ok {
			t.Errorf("no golden case matched request user message: %q", user)
			http.Error(w, "no case", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, toolUseResponse(t, match.ModelVerdict)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// userText joins the text blocks of the (single) user message in the request.
func userText(req messagesRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
	}
	return b.String()
}

// matchCase finds the case whose statement is the Claim line of the user message.
// It matches on the exact statement substring so two cases cannot collide; if the
// statements ever shared a prefix the longest match wins, keeping the mapping
// unambiguous.
func matchCase(user string, byStatement map[string]Case) (Case, bool) {
	var best Case
	found := false
	for statement, c := range byStatement {
		if strings.Contains(user, "Claim: "+statement) {
			if !found || len(statement) > len(best.Statement) {
				best = c
				found = true
			}
		}
	}
	return best, found
}

// toolUseResponse builds a minimal valid Messages response carrying one forced
// record_assessment tool call with the recorded two-axis verdict, matching the
// shape the verify package's own political tests use.
func toolUseResponse(t *testing.T, mv ModelVerdict) string {
	t.Helper()
	citations := make([]map[string]any, 0, len(mv.Citations))
	for _, c := range mv.Citations {
		citations = append(citations, map[string]any{"evidence_id": c.EvidenceID, "quoted_span": c.QuotedSpan})
	}
	flags := mv.Flags
	if flags == nil {
		flags = []string{}
	}
	input, err := json.Marshal(map[string]any{
		"literal":    mv.Literal,
		"basis":      mv.Basis,
		"flags":      flags,
		"confidence": mv.Confidence,
		"citations":  citations,
		"rationale":  mv.Rationale,
	})
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"id":    "msg_eval",
		"type":  "message",
		"role":  "assistant",
		"model": "claude-haiku-4-5-20251001",
		"content": []map[string]any{
			{"type": "tool_use", "id": "toolu_eval", "name": politicalToolName, "input": json.RawMessage(input)},
		},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(body)
}

// newVerifier points a real verify.Client at the fake recorded-model server, so
// the political path is exercised end to end (request shaping, forced tool call,
// decode, citation and flag guard) without the network.
func newVerifier(t *testing.T, g Golden) *verify.Client {
	t.Helper()
	srv := recordedModelServer(t, g)
	v, err := verify.New(verify.Config{APIKey: "test-key"}, llm.WithBaseURL(srv.URL), llm.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("verify.New: %v", err)
	}
	return v
}

func TestLoadGoldenSet(t *testing.T) {
	t.Parallel()
	g, err := LoadGolden(filepath.Clean(goldenPath))
	if err != nil {
		t.Fatalf("LoadGolden: %v", err)
	}
	if len(g.Cases) < 30 {
		t.Fatalf("golden set has %d cases, want at least 30", len(g.Cases))
	}

	// The set must exercise all three literal labels, every claim type, every
	// manipulation flag at least once, and carry the adversarial true-but-misleading
	// and same-topic-opposite-truth cases, the regression's whole point.
	literals := map[string]int{}
	flagSeen := map[string]int{}
	typeSeen := map[string]int{}
	adversarial := 0
	for _, c := range g.Cases {
		literals[c.ExpectedLiteral]++
		typeSeen[c.ClaimType]++
		for _, f := range c.ExpectedFlags {
			flagSeen[f]++
		}
		if c.Adversarial {
			adversarial++
		}
		if strings.TrimSpace(c.Provenance) == "" {
			t.Errorf("case %q has no provenance for its labels", c.ID)
		}
		if strings.TrimSpace(c.ClaimType) == "" {
			t.Errorf("case %q has no claim_type", c.ID)
		}
	}
	for _, label := range []string{LiteralAccurate, LiteralInaccurate, LiteralUnverifiable} {
		if literals[label] == 0 {
			t.Errorf("golden set carries no %q case", label)
		}
	}
	for _, flag := range []string{FlagMissingContext, FlagCherryPicked, FlagOutdated, FlagMisattributed, FlagMisleadingCausation} {
		if flagSeen[flag] == 0 {
			t.Errorf("golden set never exercises the %q flag", flag)
		}
	}
	for _, ct := range []string{"statistic", "voting-record", "attribution", "causal", "comparative", "opinion"} {
		if typeSeen[ct] == 0 {
			t.Errorf("golden set never exercises the %q claim type", ct)
		}
	}
	if adversarial < 5 {
		t.Errorf("golden set has %d adversarial cases, want at least 5", adversarial)
	}
}

// baselineLiteralAccuracy is the recorded two-axis literal accuracy of the
// political verify path over the committed golden set, replaying the recorded
// model output through the real verify.Client and its real citation/flag guard. It
// is the regression floor: the gate fails if a wiring or guard change drops literal
// accuracy below it. Recorded here so a fixture change that shifts the baseline is a
// visible, reviewed diff rather than a silent gate move.
const baselineLiteralAccuracy = 1.00

// baselineFlagAccuracy is the recorded fraction of cases whose surviving flag set
// exactly matched the expected flag set, the regression floor on the second axis.
const baselineFlagAccuracy = 1.00

// TestGoldenEvalAccuracyGate is the regression gate: it runs the two-axis political
// path over the committed French golden set and asserts both the literal verdict
// accuracy and the flag accuracy stay at or above the recorded baseline. It is
// deterministic - the path replays recorded model output through the real client
// and citation/flag guard - and needs no external API or database, so it runs in CI
// as an ordinary Go test.
func TestGoldenEvalAccuracyGate(t *testing.T) {
	t.Parallel()
	g, err := LoadGolden(filepath.Clean(goldenPath))
	if err != nil {
		t.Fatalf("LoadGolden: %v", err)
	}

	v := newVerifier(t, g)
	rep, err := RunPolitical(context.Background(), v, g)
	if err != nil {
		t.Fatalf("RunPolitical: %v", err)
	}

	t.Logf("\n%s", rep.Format("political two-axis path"))

	if got := round2(rep.LiteralAccuracy()); got < baselineLiteralAccuracy {
		t.Fatalf("literal accuracy %.2f below recorded baseline %.2f: gate failed; %s",
			got, baselineLiteralAccuracy, rep.Format("political two-axis path"))
	}
	if got := round2(rep.FlagAccuracy()); got < baselineFlagAccuracy {
		t.Fatalf("flag accuracy %.2f below recorded baseline %.2f: gate failed; %s",
			got, baselineFlagAccuracy, rep.Format("political two-axis path"))
	}
}

// round2 rounds an accuracy to two decimals so the recorded baseline constant can
// be compared against the measured value without float noise.
func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// TestValidateRecordedCitations asserts the load-time fixture guard rejects a
// recorded verdict whose citations could not survive the production citation guard
// - an unknown evidence_id, a span that is not a substring of its passage, or an
// unverifiable verdict that records a citation - so an authoring slip is a hard
// load error rather than a fixture that silently passes the gate on the wrong code
// path.
func TestValidateRecordedCitations(t *testing.T) {
	t.Parallel()

	passage := Passage{ID: "ev:0", Text: "Le taux de chômage est de 7,3%."}
	tests := []struct {
		name    string
		c       Case
		wantErr bool
	}{
		{
			name: "evidence verdict with a grounding span is accepted",
			c: Case{
				ID:           "ok-evidence",
				Passages:     []Passage{passage},
				ModelVerdict: ModelVerdict{Literal: LiteralAccurate, Basis: "evidence", Citations: []Citation{{EvidenceID: "ev:0", QuotedSpan: "taux de chômage est de 7,3%"}}},
			},
		},
		{
			name: "unknown evidence_id is rejected",
			c: Case{
				ID:           "bad-id",
				Passages:     []Passage{passage},
				ModelVerdict: ModelVerdict{Literal: LiteralAccurate, Basis: "evidence", Citations: []Citation{{EvidenceID: "ev:missing", QuotedSpan: "taux de chômage"}}},
			},
			wantErr: true,
		},
		{
			name: "span not a substring is rejected",
			c: Case{
				ID:           "bad-span",
				Passages:     []Passage{passage},
				ModelVerdict: ModelVerdict{Literal: LiteralAccurate, Basis: "evidence", Citations: []Citation{{EvidenceID: "ev:0", QuotedSpan: "inflation à 6%"}}},
			},
			wantErr: true,
		},
		{
			name: "unverifiable with a citation is rejected",
			c: Case{
				ID:           "bad-unverif",
				Passages:     []Passage{passage},
				ModelVerdict: ModelVerdict{Literal: LiteralUnverifiable, Basis: "knowledge", Citations: []Citation{{EvidenceID: "ev:0", QuotedSpan: "taux de chômage"}}},
			},
			wantErr: true,
		},
		{
			name: "unverifiable with evidence basis is rejected",
			c: Case{
				ID:           "bad-unverif-basis",
				Passages:     []Passage{passage},
				ModelVerdict: ModelVerdict{Literal: LiteralUnverifiable, Basis: "evidence"},
			},
			wantErr: true,
		},
		{
			name: "unverifiable on knowledge with no citation is accepted",
			c: Case{
				ID:           "ok-unverif",
				Passages:     []Passage{passage},
				ModelVerdict: ModelVerdict{Literal: LiteralUnverifiable, Basis: "knowledge"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateRecordedCitations(tc.c)
			if tc.wantErr && err == nil {
				t.Fatalf("validateRecordedCitations(%s) = nil, want error", tc.c.ID)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateRecordedCitations(%s) = %v, want nil", tc.c.ID, err)
			}
		})
	}
}
