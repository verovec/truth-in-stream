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
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/verovec/truth-in-stream/backend/internal/eval"
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
	return nil
}
