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

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/verovec/truth-in-stream/backend/internal/verify"
)

// goldenPath is the committed eval set the harness and the gate run over.
const goldenPath = "testdata/golden.json"

// legacyFloor is the corroboration similarity floor the legacy path used to
// decide a statement had enough support to surface. Every adversarial case is
// authored above it, which is the point: the old path surfaced those strong
// topical hits as support though the evidence refuted or did not bear on the
// claim. It is deliberately permissive so the baseline is the real old-path
// behavior, not a strawman.
const legacyFloor = 0.5

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

// recordedModelServer is a fake Anthropic Messages server that replays each
// golden case's recorded verifier tool call. It maps an incoming request to a
// case by finding the case whose statement appears as the "Claim:" line in the
// user message (verify.buildPrompt renders it there), so the verify path runs
// end to end against deterministic, per-claim model output with no network.
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
// record_verdict tool call with the recorded verdict, matching the shape the
// verify package's own tests use.
func toolUseResponse(t *testing.T, mv ModelVerdict) string {
	t.Helper()
	citations := make([]map[string]any, 0, len(mv.Citations))
	for _, c := range mv.Citations {
		citations = append(citations, map[string]any{"evidence_id": c.EvidenceID, "quoted_span": c.QuotedSpan})
	}
	input, err := json.Marshal(map[string]any{
		"verdict":    mv.Verdict,
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
			{"type": "tool_use", "id": "toolu_eval", "name": "record_verdict", "input": json.RawMessage(input)},
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
// the verify path is exercised end to end (request shaping, forced tool call,
// decode, citation guard) without the network.
func newVerifier(t *testing.T, g Golden) *verify.Client {
	t.Helper()
	srv := recordedModelServer(t, g)
	v, err := verify.New(verify.Config{APIKey: "test-key"}, option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
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
	// The set must exercise all three labels and carry adversarial
	// same-topic-opposite-truth cases, the regression's whole point.
	labels := map[string]int{}
	adversarial := 0
	for _, c := range g.Cases {
		labels[c.Expected]++
		if c.Adversarial {
			adversarial++
		}
		if strings.TrimSpace(c.Provenance) == "" {
			t.Errorf("case %q has no provenance for its label", c.ID)
		}
	}
	for _, label := range []string{VerdictSupports, VerdictRefutes, VerdictNotEnoughInfo} {
		if labels[label] == 0 {
			t.Errorf("golden set carries no %q case", label)
		}
	}
	if adversarial < 5 {
		t.Errorf("golden set has %d adversarial cases, want at least 5", adversarial)
	}
}

// baselineAccuracy is the recorded legacy similarity-only accuracy over the
// committed golden set (15/37 = 0.41), the gate the verify path must meet or
// beat. The legacy path scores every supports case right, every refutes/NEI case
// with a strong topical hit wrong (it reads similarity as support), and the one
// case with no retrievable evidence right (nothing clears the floor, so it
// reports not_enough_info). Recorded here so a fixture change that shifts the
// baseline is a visible, reviewed diff rather than a silent gate move.
const baselineAccuracy = 0.41

// TestGoldenEvalAccuracyGate is the regression gate: it runs both paths over the
// committed golden set and asserts the verify path is at least as accurate as the
// recorded legacy baseline. It is deterministic - the verify path replays
// recorded model output through the real client and citation guard - and needs no
// external API or database, so it runs in CI as an ordinary Go test.
func TestGoldenEvalAccuracyGate(t *testing.T) {
	t.Parallel()
	g, err := LoadGolden(filepath.Clean(goldenPath))
	if err != nil {
		t.Fatalf("LoadGolden: %v", err)
	}

	legacy := RunLegacy(g, legacyFloor)
	v := newVerifier(t, g)
	verifyRep, err := RunVerify(context.Background(), v, g)
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}

	t.Logf("\n%s\n%s", legacy.Format("legacy similarity path"), verifyRep.Format("retrieve-then-verify path"))

	// The recorded baseline must match the legacy path's measured accuracy, so the
	// gate cannot silently drift if the fixture changes.
	if got := round2(legacy.Accuracy()); got != baselineAccuracy {
		t.Fatalf("legacy baseline accuracy = %.2f, recorded baselineAccuracy = %.2f; update the constant in the same reviewed change as the fixture", got, baselineAccuracy)
	}

	if verifyRep.Accuracy() < legacy.Accuracy() {
		t.Fatalf("verify path accuracy %.2f below legacy baseline %.2f: gate failed", verifyRep.Accuracy(), legacy.Accuracy())
	}

	// The verify path must clear the adversarial cases the baseline gets wrong, the
	// concrete evidence the redesign fixed the similarity-is-not-entailment bug.
	if verifyRep.Accuracy() <= legacy.Accuracy() {
		t.Fatalf("verify path accuracy %.2f only ties the baseline %.2f: the eval set is not exercising the improvement", verifyRep.Accuracy(), legacy.Accuracy())
	}
}

// round2 rounds an accuracy to two decimals so the recorded baseline constant can
// be compared exactly against the measured value without float noise.
func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
