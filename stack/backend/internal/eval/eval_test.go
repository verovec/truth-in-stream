package eval

import (
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

// recordedVerdict marshals one golden case's recorded two-axis verdict into the
// flat structured object both providers' tool-call carriers wrap. It is the single
// source of the replayed answer, so the Anthropic and Gemini fakes cannot drift in
// what they return for a case.
func recordedVerdict(mv ModelVerdict) map[string]any {
	citations := make([]map[string]any, 0, len(mv.Citations))
	for _, c := range mv.Citations {
		citations = append(citations, map[string]any{"evidence_id": c.EvidenceID, "quoted_span": c.QuotedSpan})
	}
	flags := mv.Flags
	if flags == nil {
		flags = []string{}
	}
	return map[string]any{
		"literal":    mv.Literal,
		"basis":      mv.Basis,
		"flags":      flags,
		"confidence": mv.Confidence,
		"citations":  citations,
		"rationale":  mv.Rationale,
	}
}

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

// anthropicFakeServer is a fake Anthropic Messages server that replays each golden
// case's recorded two-axis verifier tool call. It maps an incoming request to a
// case by finding the case whose statement appears as the "Claim:" line in the user
// message (verify.buildPrompt renders it there, shared by the political path), so
// the political path runs end to end against deterministic, per-claim model output
// with no network.
func anthropicFakeServer(t *testing.T, byStatement map[string]Case) *httptest.Server {
	t.Helper()
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
		match, ok := matchCase(anthropicUserText(req), byStatement)
		if !ok {
			t.Errorf("no golden case matched anthropic request")
			http.Error(w, "no case", http.StatusBadRequest)
			return
		}
		input, err := json.Marshal(recordedVerdict(match.ModelVerdict))
		if err != nil {
			t.Errorf("marshal tool input: %v", err)
			http.Error(w, "marshal", http.StatusInternalServerError)
			return
		}
		resp, err := json.Marshal(map[string]any{
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
			t.Errorf("marshal response: %v", err)
			http.Error(w, "marshal", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(resp); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// geminiRequest is the slice of the Gemini GenerateContent request body the fake
// reads: the user-content text parts (to recover the claim) and the tool-calling
// config (to assert the forced single function call).
type geminiRequest struct {
	Contents []struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"contents"`
	ToolConfig struct {
		FunctionCallingConfig struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig"`
}

// geminiFakeServer is a fake Gemini GenerateContent server that replays each golden
// case's recorded verdict as a forced function call, the Gemini wire twin of the
// Anthropic fake. It recovers the claim from the request's user-content parts, then
// asserts the request actually forced the single named function (mode ANY with the
// one allowed name) before answering, so the test proves the eval drives Gemini
// under the same forced-call regime production uses rather than a free-form reply.
func geminiFakeServer(t *testing.T, byStatement map[string]Case) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		var req geminiRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "decode", http.StatusBadRequest)
			return
		}
		fc := req.ToolConfig.FunctionCallingConfig
		if fc.Mode != "ANY" || len(fc.AllowedFunctionNames) != 1 || fc.AllowedFunctionNames[0] != politicalToolName {
			t.Errorf("gemini request did not force the single %q function call: mode=%q allowed=%v",
				politicalToolName, fc.Mode, fc.AllowedFunctionNames)
			http.Error(w, "not forced", http.StatusBadRequest)
			return
		}
		match, ok := matchCase(geminiUserText(req), byStatement)
		if !ok {
			t.Errorf("no golden case matched gemini request")
			http.Error(w, "no case", http.StatusBadRequest)
			return
		}
		resp, err := json.Marshal(map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"role":  "model",
					"parts": []map[string]any{{"functionCall": map[string]any{"name": politicalToolName, "args": recordedVerdict(match.ModelVerdict)}}},
				},
				"finishReason": "STOP",
			}},
		})
		if err != nil {
			t.Errorf("marshal response: %v", err)
			http.Error(w, "marshal", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(resp); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// deepseekRequest is the slice of the OpenAI-compatible Chat Completions request
// body the DeepSeek fake reads: the message texts (to recover the claim) and the
// tool_choice (to assert the forced single named function call).
type deepseekRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	ToolChoice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tool_choice"`
}

// deepseekFakeServer is a fake OpenAI-compatible Chat Completions server that
// replays each golden case's recorded verdict as a forced tool call, the DeepSeek
// wire twin of the Anthropic and Gemini fakes. It recovers the claim from the
// request's message texts, then asserts the request actually forced the single
// named function (a tool_choice function object naming politicalToolName) before
// answering, so the test proves the eval drives DeepSeek under the same
// forced-call regime production uses rather than a free-form reply.
func deepseekFakeServer(t *testing.T, byStatement map[string]Case) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		var req deepseekRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "decode", http.StatusBadRequest)
			return
		}
		if req.ToolChoice.Type != "function" || req.ToolChoice.Function.Name != politicalToolName {
			t.Errorf("deepseek request did not force the single %q tool call: type=%q name=%q",
				politicalToolName, req.ToolChoice.Type, req.ToolChoice.Function.Name)
			http.Error(w, "not forced", http.StatusBadRequest)
			return
		}
		match, ok := matchCase(deepseekUserText(req), byStatement)
		if !ok {
			t.Errorf("no golden case matched deepseek request")
			http.Error(w, "no case", http.StatusBadRequest)
			return
		}
		args, err := json.Marshal(recordedVerdict(match.ModelVerdict))
		if err != nil {
			t.Errorf("marshal tool args: %v", err)
			http.Error(w, "marshal", http.StatusInternalServerError)
			return
		}
		resp, err := json.Marshal(map[string]any{
			"id": "chatcmpl_eval", "object": "chat.completion", "created": 1, "model": "deepseek-v4-flash",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":       "assistant",
					"tool_calls": []map[string]any{{"id": "call_eval", "type": "function", "function": map[string]any{"name": politicalToolName, "arguments": string(args)}}},
				},
				"finish_reason": "tool_calls",
			}},
		})
		if err != nil {
			t.Errorf("marshal response: %v", err)
			http.Error(w, "marshal", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(resp); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// anthropicUserText joins the text blocks of the (single) user message in the
// Anthropic request.
func anthropicUserText(req messagesRequest) string {
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

// geminiUserText joins the text parts of the request's user contents. The system
// frame is carried separately (systemInstruction), so this is the rendered claim
// prompt the matcher keys on.
func geminiUserText(req geminiRequest) string {
	var b strings.Builder
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// deepseekUserText joins the content of the request's user messages. The system
// frame is a separate message (role "system"), so only the user content carries
// the rendered claim prompt the matcher keys on.
func deepseekUserText(req deepseekRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if m.Role == "user" {
			b.WriteString(m.Content)
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

// indexByStatement indexes the golden set by claim, the key both fakes match a
// request against.
func indexByStatement(g Golden) map[string]Case {
	m := make(map[string]Case, len(g.Cases))
	for _, c := range g.Cases {
		m[c.Statement] = c
	}
	return m
}

// newVerifier builds a real verify.Client for the target's provider, pointed at a
// per-provider fake server that replays the golden set's recorded verdicts. The
// political path is exercised end to end (request shaping, forced tool/function
// call, decode, citation and flag guard) against the selected provider's wire
// format without the network, so the same golden set scores identically under
// either backend - the offline shape of the real-model comparison.
func newVerifier(t *testing.T, g Golden, target Target) *verify.Client {
	t.Helper()
	index := indexByStatement(g)
	const fakeKey = "test-key"

	var srv *httptest.Server
	switch target.Provider {
	case llm.ProviderGemini:
		srv = geminiFakeServer(t, index)
	case llm.ProviderAnthropic:
		srv = anthropicFakeServer(t, index)
	default:
		// Empty provider defaults to DeepSeek, matching llm.NewClient.
		srv = deepseekFakeServer(t, index)
	}

	v, err := verify.New(target.VerifierConfig(fakeKey), llm.WithBaseURL(srv.URL), llm.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("verify.New (%s): %v", target.Provider, err)
	}
	return v
}

// goldenTargets is the set of provider selections the offline gate runs the golden
// set under: the DeepSeek default (empty provider), Anthropic, and Gemini. All are
// faked; running every backend proves the harness scores the same golden set
// identically regardless of provider and that provider selection wires end to end.
var goldenTargets = []struct {
	name   string
	target Target
}{
	{name: "deepseek-default", target: Target{}},
	{name: "anthropic", target: Target{Provider: llm.ProviderAnthropic}},
	{name: "gemini", target: Target{Provider: llm.ProviderGemini}},
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
// path over the committed French golden set under each supported provider and
// asserts both the literal verdict accuracy and the flag accuracy stay at or above
// the recorded baseline. It is deterministic - each provider's path replays the same
// recorded verdict through the real client and citation/flag guard - and needs no
// external API or database, so it runs in CI as an ordinary Go test. Running it
// under both the Anthropic default and Gemini proves the same golden set scores
// identically under either backend, the offline guarantee behind the real-model
// comparison.
func TestGoldenEvalAccuracyGate(t *testing.T) {
	t.Parallel()
	g, err := LoadGolden(filepath.Clean(goldenPath))
	if err != nil {
		t.Fatalf("LoadGolden: %v", err)
	}

	for _, tc := range goldenTargets {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := newVerifier(t, g, tc.target)
			rep, err := RunPolitical(t.Context(), v, g)
			if err != nil {
				t.Fatalf("RunPolitical: %v", err)
			}

			t.Logf("\n%s", rep.Format("political two-axis path ("+tc.name+")"))

			if got := round2(rep.LiteralAccuracy()); got < baselineLiteralAccuracy {
				t.Fatalf("literal accuracy %.2f below recorded baseline %.2f: gate failed; %s",
					got, baselineLiteralAccuracy, rep.Format("political two-axis path"))
			}
			if got := round2(rep.FlagAccuracy()); got < baselineFlagAccuracy {
				t.Fatalf("flag accuracy %.2f below recorded baseline %.2f: gate failed; %s",
					got, baselineFlagAccuracy, rep.Format("political two-axis path"))
			}
		})
	}
}

// TestGoldenGateFailsOnInjectedRegression proves the gate has teeth under every
// provider: it corrupts one golden case's recorded verdict (flipping an accurate
// literal to inaccurate) so the replayed model now disagrees with the label, runs
// the same path the gate runs under each provider, and asserts the measured literal
// accuracy drops below the baseline. A gate that could not catch this is not a gate.
func TestGoldenGateFailsOnInjectedRegression(t *testing.T) {
	t.Parallel()
	g, err := LoadGolden(filepath.Clean(goldenPath))
	if err != nil {
		t.Fatalf("LoadGolden: %v", err)
	}

	corrupted := corruptOneAccurateCase(t, g)

	for _, tc := range goldenTargets {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := newVerifier(t, corrupted, tc.target)
			rep, err := RunPolitical(t.Context(), v, corrupted)
			if err != nil {
				t.Fatalf("RunPolitical: %v", err)
			}
			if got := round2(rep.LiteralAccuracy()); got >= baselineLiteralAccuracy {
				t.Fatalf("injected regression under %s did not drop literal accuracy below baseline: got %.2f >= %.2f; %s",
					tc.name, got, baselineLiteralAccuracy, rep.Format("injected regression"))
			}
		})
	}
}

// corruptOneAccurateCase returns a copy of g with the first accurate-literal case's
// recorded verdict flipped to inaccurate, simulating a model (or wiring) regression
// the gate must catch. The expected label is left untouched, so the flipped verdict
// is now wrong against it. It fails the test if the set has no accurate case to
// corrupt, since the regression check would otherwise be silently vacuous.
func corruptOneAccurateCase(t *testing.T, g Golden) Golden {
	t.Helper()
	cases := make([]Case, len(g.Cases))
	copy(cases, g.Cases)
	for i := range cases {
		if cases[i].ExpectedLiteral != LiteralAccurate || cases[i].ModelVerdict.Literal != LiteralAccurate {
			continue
		}
		cases[i].ModelVerdict.Literal = LiteralInaccurate
		return Golden{About: g.About, Cases: cases}
	}
	t.Fatalf("golden set has no accurate case to corrupt; cannot exercise the regression gate")
	return Golden{}
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

// TestTargetVerifierConfig asserts the Target threads the supplied key onto the
// field the selected provider reads - GeminiAPIKey under Gemini, APIKey otherwise -
// and carries the model through, so a real-model run keys the right provider from
// the environment.
func TestTargetVerifierConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		target       Target
		wantAPIKey   string
		wantGemKey   string
		wantDeepKey  string
		wantProvider llm.ProviderName
	}{
		{
			name:        "default targets deepseek and keys DeepSeekAPIKey",
			target:      Target{Model: "deepseek-v4-flash"},
			wantDeepKey: "secret",
		},
		{
			name:         "anthropic keys APIKey",
			target:       Target{Provider: llm.ProviderAnthropic, Model: "claude-haiku-4-5-20251001"},
			wantAPIKey:   "secret",
			wantProvider: llm.ProviderAnthropic,
		},
		{
			name:         "gemini keys GeminiAPIKey",
			target:       Target{Provider: llm.ProviderGemini, Model: "gemini-2.5-flash"},
			wantGemKey:   "secret",
			wantProvider: llm.ProviderGemini,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := tc.target.VerifierConfig("secret")
			if cfg.Provider != tc.wantProvider {
				t.Errorf("Provider = %q, want %q", cfg.Provider, tc.wantProvider)
			}
			if cfg.APIKey != tc.wantAPIKey {
				t.Errorf("APIKey = %q, want %q", cfg.APIKey, tc.wantAPIKey)
			}
			if cfg.GeminiAPIKey != tc.wantGemKey {
				t.Errorf("GeminiAPIKey = %q, want %q", cfg.GeminiAPIKey, tc.wantGemKey)
			}
			if cfg.DeepSeekAPIKey != tc.wantDeepKey {
				t.Errorf("DeepSeekAPIKey = %q, want %q", cfg.DeepSeekAPIKey, tc.wantDeepKey)
			}
			if cfg.Model != tc.target.Model {
				t.Errorf("Model = %q, want %q", cfg.Model, tc.target.Model)
			}
		})
	}
}
