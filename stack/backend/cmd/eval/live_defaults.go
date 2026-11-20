package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/checkworthy"
	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/eval"
	"github.com/verovec/truth-in-stream/backend/internal/llm"
	"github.com/verovec/truth-in-stream/backend/internal/localworthy"
	"github.com/verovec/truth-in-stream/backend/internal/nli"
	"github.com/verovec/truth-in-stream/backend/internal/verify"
)

// defaultsComparison runs the decision stages the vector-first defaults
// changed - the check-worthiness gate and the evidence verdict - over the
// committed French fixtures with the real local scorers and the real
// generative models, once per configuration:
//
//   - legacy: heuristic then generative gate; generative verifier for every
//     verdict (the pre-epic default path).
//   - vector-first: heuristic then local classifier band (generative only in
//     band); NLI consensus first, generative verifier only on escalation.
//
// It records accuracy against the fixture labels, generative calls, and
// latency per configuration. Retrieval quality is measured by the offline
// retrieval gate and the -rerank comparison; the streaming transcription leg
// is configuration-invariant. Live: needs DEEPSEEK_API_KEY, the localinference
// build tag, and the model artifacts via the CHECKWORTHINESS_LOCAL_* and
// FACTCHECK_NLI_* environment.
type defaultsComparison struct {
	gateGolden eval.GateGolden
	nliGolden  eval.NLIGolden
	out        io.Writer
}

// stageStats accumulates one configuration's run over one stage.
type stageStats struct {
	Cases    int
	Correct  int
	LLMCalls int
	Elapsed  time.Duration
}

func (s stageStats) accuracy() float64 {
	if s.Cases == 0 {
		return 0
	}
	return float64(s.Correct) / float64(s.Cases)
}

func (s stageStats) callRate() float64 {
	if s.Cases == 0 {
		return 0
	}
	return float64(s.LLMCalls) / float64(s.Cases)
}

func runDefaultsComparison(gatePath, nliPath string, out io.Writer) error {
	gateGolden, err := eval.LoadGateGolden(gatePath)
	if err != nil {
		return err
	}
	nliGolden, err := eval.LoadNLIGolden(nliPath)
	if err != nil {
		return err
	}
	c := defaultsComparison{gateGolden: gateGolden, nliGolden: nliGolden, out: out}

	deepseekKey := os.Getenv("DEEPSEEK_API_KEY")
	if deepseekKey == "" {
		return fmt.Errorf("defaults comparison needs DEEPSEEK_API_KEY")
	}
	gateModel, err := checkworthy.New(checkworthy.Config{Provider: llm.ProviderDeepSeek, DeepSeekAPIKey: deepseekKey, Locale: domain.LocaleFrench})
	if err != nil {
		return err
	}
	verifier, err := verify.New(verify.Config{Provider: llm.ProviderDeepSeek, DeepSeekAPIKey: deepseekKey, Locale: domain.LocaleFrench})
	if err != nil {
		return err
	}

	localCfg, err := config.LoadCheckWorthinessLocal()
	if err != nil {
		return err
	}
	nliCfg, err := config.LoadVerifyNLI()
	if err != nil {
		return err
	}
	if !localCfg.Active() || !nliCfg.Active() {
		return fmt.Errorf("defaults comparison needs the CHECKWORTHINESS_LOCAL_* and FACTCHECK_NLI_* artifacts configured")
	}
	localScorer, err := localworthy.New(localworthy.Config{ModelPath: localCfg.ModelPath, TokenizerPath: localCfg.TokenizerPath, LibraryPath: localCfg.LibraryPath, Timeout: localCfg.Timeout})
	if err != nil {
		return fmt.Errorf("local check-worthiness scorer: %w", err)
	}
	defer func() { _ = localScorer.Close() }()
	nliScorer, err := nli.New(nli.Config{ModelPath: nliCfg.ModelPath, TokenizerPath: nliCfg.TokenizerPath, LibraryPath: nliCfg.LibraryPath, Temperature: nliCfg.Temperature, Timeout: nliCfg.Timeout})
	if err != nil {
		return fmt.Errorf("nli stance scorer: %w", err)
	}
	defer func() { _ = nliScorer.Close() }()

	ctx := context.Background()

	legacyGate := c.runGateLegacy(ctx, gateModel)
	newGate := c.runGateVectorFirst(ctx, localScorer, gateModel, localCfg)
	legacyVerdict := c.runVerdictLegacy(ctx, verifier)
	newVerdict := c.runVerdictVectorFirst(ctx, nliScorer, verifier, nliCfg)

	report := "defaults comparison over the committed French fixtures (live models):\n\n"
	report += fmt.Sprintf("gate stage (%d statements):\n", len(c.gateGolden.Cases))
	report += formatStageRow("legacy (heuristic + generative gate)", legacyGate)
	report += formatStageRow("vector-first (local band)", newGate)
	report += fmt.Sprintf("\nverdict stage (%d claims):\n", len(c.nliGolden.Cases))
	report += formatStageRow("legacy (generative verifier)", legacyVerdict)
	report += formatStageRow("vector-first (NLI consensus first)", newVerdict)
	report += fmt.Sprintf("\ntotal generative calls: legacy %d, vector-first %d (%.0f%% reduction)\n",
		legacyGate.LLMCalls+legacyVerdict.LLMCalls,
		newGate.LLMCalls+newVerdict.LLMCalls,
		100*(1-float64(newGate.LLMCalls+newVerdict.LLMCalls)/float64(legacyGate.LLMCalls+legacyVerdict.LLMCalls)))
	_, err = io.WriteString(out, report)
	return err
}

func formatStageRow(name string, s stageStats) string {
	return fmt.Sprintf("  %-40s accuracy %.3f  llm calls %d (%.3f/case)  mean latency %s\n",
		name, s.accuracy(), s.LLMCalls, s.callRate(), (s.Elapsed / time.Duration(max(1, s.Cases))).Round(time.Millisecond))
}

// runGateLegacy scores every gate statement with the generative gate alone
// (the heuristic already passed these statements by construction).
func (c defaultsComparison) runGateLegacy(ctx context.Context, gate *checkworthy.Client) stageStats {
	s := stageStats{Cases: len(c.gateGolden.Cases)}
	start := time.Now()
	for _, gc := range c.gateGolden.Cases {
		worthy, err := gate.CheckWorthy(ctx, gc.Text)
		s.LLMCalls++
		if err != nil {
			continue
		}
		if worthy == (gc.Label == "claim") {
			s.Correct++
		}
	}
	s.Elapsed = time.Since(start)
	return s
}

// runGateVectorFirst scores locally and consults the generative gate only
// inside the configured band.
func (c defaultsComparison) runGateVectorFirst(ctx context.Context, scorer *localworthy.Scorer, gate *checkworthy.Client, cfg config.CheckWorthinessLocal) stageStats {
	s := stageStats{Cases: len(c.gateGolden.Cases)}
	start := time.Now()
	for _, gc := range c.gateGolden.Cases {
		score, err := scorer.Score(ctx, gc.Text)
		var worthy bool
		switch {
		case err == nil && score < cfg.BandLow:
			worthy = false
		case err == nil && score >= cfg.BandHigh:
			worthy = true
		default:
			verdict, gateErr := gate.CheckWorthy(ctx, gc.Text)
			s.LLMCalls++
			if gateErr != nil {
				continue
			}
			worthy = verdict
		}
		if worthy == (gc.Label == "claim") {
			s.Correct++
		}
	}
	s.Elapsed = time.Since(start)
	return s
}

// verdictCorrect maps a credibility verdict back onto the stance fixture's
// label vocabulary.
func verdictCorrect(verdict, label string) bool {
	switch label {
	case "support":
		return verdict == verify.VerdictCredible
	case "refute":
		return verdict == verify.VerdictDisputed
	default:
		return verdict == verify.VerdictUnverifiable
	}
}

// fixturePassages projects one stance case's recorded evidence into verifier
// passages.
func fixturePassages(nc eval.NLICase) []verify.Passage {
	passages := make([]verify.Passage, len(nc.Passages))
	for i, p := range nc.Passages {
		passages[i] = verify.Passage{ID: p.ID, Text: p.Text}
	}
	return passages
}

// runVerdictLegacy sends every claim with its fixture evidence to the
// generative verifier.
func (c defaultsComparison) runVerdictLegacy(ctx context.Context, verifier *verify.Client) stageStats {
	s := stageStats{Cases: len(c.nliGolden.Cases)}
	start := time.Now()
	for _, nc := range c.nliGolden.Cases {
		passages := fixturePassages(nc)
		res, err := verifier.Verify(ctx, nc.Claim, passages)
		s.LLMCalls++
		if err != nil {
			continue
		}
		res = verify.ValidateCitations(res, passages)
		if verdictCorrect(res.Verdict, nc.Label) {
			s.Correct++
		}
	}
	s.Elapsed = time.Since(start)
	return s
}

// runVerdictVectorFirst tries the NLI consensus first and escalates only the
// undecided claims to the generative verifier.
func (c defaultsComparison) runVerdictVectorFirst(ctx context.Context, scorer *nli.Scorer, verifier *verify.Client, cfg config.VerifyNLI) stageStats {
	s := stageStats{Cases: len(c.nliGolden.Cases)}
	start := time.Now()
	for _, nc := range c.nliGolden.Cases {
		passages := fixturePassages(nc)
		texts := make([]string, len(passages))
		for i, p := range passages {
			texts[i] = p.Text
		}
		verdict := ""
		stances, err := scorer.ScoreStances(ctx, nc.Claim, texts)
		if err == nil {
			entails, contradicts := 0, 0
			for _, st := range stances {
				if st.Entailment >= cfg.EntailThreshold {
					entails++
				}
				if st.Contradiction >= cfg.ContradictThreshold {
					contradicts++
				}
			}
			switch {
			case entails >= cfg.MinAgree && contradicts == 0:
				verdict = verify.VerdictCredible
			case contradicts >= cfg.MinAgree && entails == 0:
				verdict = verify.VerdictDisputed
			}
		}
		if verdict == "" {
			res, verifyErr := verifier.Verify(ctx, nc.Claim, passages)
			s.LLMCalls++
			if verifyErr != nil {
				continue
			}
			res = verify.ValidateCitations(res, passages)
			verdict = res.Verdict
		}
		if verdictCorrect(verdict, nc.Label) {
			s.Correct++
		}
	}
	s.Elapsed = time.Since(start)
	return s
}
