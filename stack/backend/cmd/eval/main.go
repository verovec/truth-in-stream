// Command eval runs the French political retrieval eval gate offline: it loads
// the committed golden set and baseline, scores the deterministic lexical
// retrieval oracle's recall@1/recall@3 per retrieval-stress category over the
// seeded evidence passages, prints the per-category report, and exits non-zero if
// any category (or the overall) recall@1 falls below its committed floor. It needs
// no network, no model API, and no database - the golden passages are the seeded
// corpus and the baseline is a reviewed file - so it is the one command an
// operator (and CI) runs to prove a retrieval change did not regress recall. The
// two-axis verdict accuracy gate runs alongside as an ordinary `go test` over
// internal/eval; see internal/eval/README.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/eval"
	"github.com/verovec/truth-in-stream/backend/internal/rerank"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "eval:", err)
		os.Exit(1)
	}
}

// run loads the golden set and baseline, scores retrieval recall, writes the
// report to out, and returns a non-nil error when the gate fails (a missed floor)
// or an input is malformed, so main can map it to a non-zero exit.
func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(out)
	goldenPath := fs.String("golden", filepath.Join("internal", "eval", "testdata", "golden.json"), "path to the golden set")
	baselinePath := fs.String("baseline", filepath.Join("internal", "eval", "testdata", "baseline.json"), "path to the committed baseline")
	gatePath := fs.String("gate", filepath.Join("internal", "eval", "testdata", "gate_golden.json"), "path to the check-worthiness gate fixture (required when the baseline carries a gate section)")
	nliPath := fs.String("nli", filepath.Join("internal", "eval", "testdata", "nli_golden.json"), "path to the nli stance fixture (required when the baseline carries an nli section)")
	rerankOn := fs.Bool("rerank", false, "also score the live Voyage reranker over the same cases (needs RERANK_API_KEY or EMBEDDING_API_KEY; informational, not a gate)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	g, err := eval.LoadGolden(*goldenPath)
	if err != nil {
		return err
	}
	base, err := eval.LoadBaseline(*baselinePath)
	if err != nil {
		return err
	}

	rep := eval.RunRetrieval(g)
	report := rep.Format()

	failures := base.CheckRetrieval(rep)
	if len(failures) > 0 {
		report += fmt.Sprintf("\n\nFAIL: retrieval recall below baseline:%s\n", eval.FormatFailures(failures))
		if _, err := io.WriteString(out, report); err != nil {
			return err
		}
		return fmt.Errorf("retrieval recall gate failed with %d missed floor(s)", len(failures))
	}
	report += "\n\nPASS: retrieval recall meets every committed floor\n"
	if _, err := io.WriteString(out, report); err != nil {
		return err
	}
	if base.Gate != nil {
		if err := runGateCheck(base, *gatePath, out); err != nil {
			return err
		}
	}
	if base.NLI != nil {
		if err := runNLICheck(base, *nliPath, out); err != nil {
			return err
		}
	}
	if *rerankOn {
		return runRerankComparison(g, rep, out)
	}
	return nil
}

// runGateCheck replays the recorded check-worthiness gate fixture against the
// baseline's gate floors: cascade accuracy, outside-band agreement with the
// generative gate, and the model call rate. Everything is recomputed from the
// committed fixture, so the check is fully offline.
func runGateCheck(base eval.Baseline, gatePath string, out io.Writer) error {
	gg, err := eval.LoadGateGolden(gatePath)
	if err != nil {
		return err
	}
	rep := eval.RunGate(gg)
	report := "\n" + rep.Format()
	failures := base.CheckGate(rep)
	if len(failures) > 0 {
		report += fmt.Sprintf("\n\nFAIL: check-worthiness gate outside baseline bounds:%s\n", eval.FormatGateFailures(failures))
		if _, err := io.WriteString(out, report); err != nil {
			return err
		}
		return fmt.Errorf("check-worthiness gate failed with %d missed floor(s)", len(failures))
	}
	report += "\n\nPASS: check-worthiness gate meets every committed floor\n"
	_, err = io.WriteString(out, report)
	return err
}

// runNLICheck replays the recorded NLI stance fixture against the baseline's
// floors and the hard negation invariant: locally-decided accuracy, local
// share, and never the same stance for a claim and its negation. Fully
// offline, like the gate check.
func runNLICheck(base eval.Baseline, nliPath string, out io.Writer) error {
	g, err := eval.LoadNLIGolden(nliPath)
	if err != nil {
		return err
	}
	rep := eval.RunNLI(g)
	report := "\n" + rep.Format()
	failures := base.CheckNLI(rep)
	if len(failures) > 0 {
		report += fmt.Sprintf("\n\nFAIL: nli stance stage outside baseline bounds:%s\n", eval.FormatGateFailures(failures))
		if _, err := io.WriteString(out, report); err != nil {
			return err
		}
		return fmt.Errorf("nli stance gate failed with %d missed bound(s)", len(failures))
	}
	report += "\n\nPASS: nli stance stage meets every committed floor\n"
	_, err = io.WriteString(out, report)
	return err
}

// runRerankComparison scores the live Voyage reranker over the same golden
// cases and prints its report beside the oracle's. It is informational - the
// committed gate stays the offline oracle - but it fails loud on a missing key
// or an API error, because a silent fallback would present oracle numbers as
// reranker numbers.
func runRerankComparison(g eval.Golden, oracle eval.RetrievalReport, out io.Writer) error {
	key := os.Getenv("RERANK_API_KEY")
	if key == "" {
		key = os.Getenv("EMBEDDING_API_KEY")
	}
	if key == "" {
		return fmt.Errorf("rerank comparison needs RERANK_API_KEY or EMBEDDING_API_KEY")
	}
	model := os.Getenv("MATCH_RERANK_MODEL")
	if model == "" {
		model = rerank.DefaultModel
	}
	client, err := rerank.New(rerank.Config{APIKey: key, Model: model})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	reranked, err := eval.RunRetrievalReranked(ctx, g, client)
	if err != nil {
		return err
	}
	comparison := fmt.Sprintf("\nreranker (%s) over the same cases:\n\n%s\n\ndelta overall: R@1 %+.1f pts, R@3 %+.1f pts\n",
		model, reranked.Format(),
		(reranked.OverallAt1-oracle.OverallAt1)*100,
		(reranked.OverallAt3-oracle.OverallAt3)*100)
	_, err = io.WriteString(out, comparison)
	return err
}
