package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoTestdata resolves the committed golden and baseline paths from the package
// directory so the test runs regardless of the working directory.
func repoTestdata(t *testing.T) (golden, baseline string) {
	t.Helper()
	base := filepath.Join("..", "..", "internal", "eval", "testdata")
	return filepath.Join(base, "golden.json"), filepath.Join(base, "baseline.json")
}

// TestRunPassesOnCommittedBaseline is the end-to-end check: the eval command runs
// green over the committed golden set and baseline, printing the per-category
// report and a PASS line.
func TestRunPassesOnCommittedBaseline(t *testing.T) {
	golden, baseline := repoTestdata(t)
	var out strings.Builder
	if err := run([]string{"-golden", golden, "-baseline", baseline}, &out); err != nil {
		t.Fatalf("run over committed data failed: %v\n%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "PASS:") {
		t.Errorf("output missing PASS line:\n%s", got)
	}
	for _, cat := range []string{"number-precision", "named-entity", "date-anchored", "paraphrase", "near-miss"} {
		if !strings.Contains(got, cat) {
			t.Errorf("output missing category %q:\n%s", cat, got)
		}
	}
}

// TestRunFailsOnRaisedFloor proves the command's non-zero exit path: a baseline
// demanding perfect recall on every category fails, because the paraphrase
// category deliberately falls short, and the output names the regression.
func TestRunFailsOnRaisedFloor(t *testing.T) {
	golden, _ := repoTestdata(t)
	impossible := filepath.Join(t.TempDir(), "baseline.json")
	body := `{"retrieval":{"min_overall_recall_at_1":1.0,"categories":{` +
		`"number-precision":1.0,"named-entity":1.0,"date-anchored":1.0,"paraphrase":1.0,"near-miss":1.0}}}`
	if err := os.WriteFile(impossible, []byte(body), 0o600); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	var out strings.Builder
	err := run([]string{"-golden", golden, "-baseline", impossible}, &out)
	if err == nil {
		t.Fatalf("run with an impossible baseline succeeded, want failure\n%s", out.String())
	}
	if !strings.Contains(out.String(), "FAIL:") {
		t.Errorf("output missing FAIL line:\n%s", out.String())
	}
}
