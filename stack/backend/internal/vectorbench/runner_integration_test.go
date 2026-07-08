package vectorbench

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// integrationConfig is small enough to run in seconds but exercises every
// scenario, both filters, and the iterative-scan gate end to end.
func integrationConfig() Config {
	return Config{
		Corpus: CorpusConfig{
			Rows:     400,
			Queries:  8,
			Dims:     64,
			Sources:  3,
			Clusters: 6,
			Seed:     7,
		},
		Matrix: MatrixConfig{
			Scenarios:   []Scenario{ScenarioHNSW, ScenarioBQ, ScenarioPartitioned},
			Filters:     []Filter{FilterNone, FilterSource},
			EfSearch:    []int{40},
			Iterative:   []string{IterativeOff, IterativeRelaxed},
			Multipliers: []int{4},
		},
		K: 5,
	}
}

func openBenchPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping pgvector integration test")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestRunEndToEnd(t *testing.T) {
	pool := openBenchPool(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := integrationConfig()

	report, err := Run(t.Context(), pool, cfg, logger)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.PgvectorVersion == "" {
		t.Error("report has no pgvector version")
	}
	// 3 scenarios x 2 filters x 1 ef x 2 iterative (+ bq multiplier of 1 value).
	if got, want := len(report.Results), 12; got != want {
		t.Fatalf("got %d results, want %d", got, want)
	}
	for _, r := range report.Results {
		if r.Skipped != "" {
			t.Logf("cell skipped: %s (%s)", r.Cell.Label(), r.Skipped)
			continue
		}
		if r.Recall < 0 || r.Recall > 1 {
			t.Errorf("%s: recall %f out of range", r.Cell.Label(), r.Recall)
		}
		if r.P50 <= 0 || r.P95 <= 0 || r.P95 < r.P50 {
			t.Errorf("%s: implausible latencies p50=%v p95=%v", r.Cell.Label(), r.P50, r.P95)
		}
	}
	// The production-baseline cell on a 400-row corpus must be near-exact.
	baseline := findResult(t, report.Results, Cell{
		Scenario: ScenarioHNSW, Filter: FilterNone, EfSearch: 40, Iterative: IterativeOff,
	})
	if baseline.Recall < 0.9 {
		t.Errorf("baseline hnsw recall = %f, want >= 0.9", baseline.Recall)
	}
	wantFootprints := []string{
		"single table",
		"single hnsw index (halfvec)",
		"bit expression index (binary quantize)",
		"partitioned tables (total)",
		"partitioned hnsw indexes (total)",
	}
	if len(report.Footprints) != len(wantFootprints) {
		t.Fatalf("got %d footprints (%v), want %d", len(report.Footprints), report.Footprints, len(wantFootprints))
	}
	for i, want := range wantFootprints {
		if report.Footprints[i].Name != want {
			t.Errorf("footprint[%d] = %q, want %q", i, report.Footprints[i].Name, want)
		}
		if report.Footprints[i].Bytes <= 0 {
			t.Errorf("footprint %q has no size", want)
		}
	}
	assertSchemaAbsent(t, pool)
}

func TestRunKeepsSchemaWhenAsked(t *testing.T) {
	pool := openBenchPool(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := integrationConfig()
	cfg.Keep = true
	cfg.Matrix.Scenarios = []Scenario{ScenarioHNSW}
	cfg.Matrix.Filters = []Filter{FilterNone}
	cfg.Matrix.Iterative = []string{IterativeOff}

	if _, err := Run(t.Context(), pool, cfg, logger); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var count int
	if err := pool.QueryRow(t.Context(),
		"SELECT count(*) FROM pg_namespace WHERE nspname = 'vectorbench'").Scan(&count); err != nil {
		t.Fatalf("checking schema: %v", err)
	}
	if count != 1 {
		t.Error("vectorbench schema was dropped despite Keep")
	}
	if _, err := pool.Exec(t.Context(), "DROP SCHEMA IF EXISTS vectorbench CASCADE"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

func findResult(t *testing.T, results []Result, cell Cell) Result {
	t.Helper()
	for _, r := range results {
		if r.Cell == cell {
			return r
		}
	}
	t.Fatalf("no result for cell %s", cell.Label())
	return Result{}
}

func assertSchemaAbsent(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM pg_namespace WHERE nspname = 'vectorbench'").Scan(&count); err != nil {
		t.Fatalf("checking schema: %v", err)
	}
	if count != 0 {
		t.Error("vectorbench schema still present after run")
	}
}
