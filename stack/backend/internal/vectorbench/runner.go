package vectorbench

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config is the full benchmark configuration: what corpus to generate, which
// cells to measure, and how many neighbors each search returns.
type Config struct {
	Corpus CorpusConfig
	Matrix MatrixConfig
	K      int
	// Keep leaves the vectorbench schema in place after the run for manual
	// inspection; by default the schema is dropped.
	Keep bool
}

func (c Config) validate() error {
	if err := c.Corpus.validate(); err != nil {
		return err
	}
	if err := c.Matrix.validate(); err != nil {
		return err
	}
	if c.K <= 0 {
		return fmt.Errorf("vectorbench: k must be positive, got %d", c.K)
	}
	if c.K > c.Corpus.Rows {
		return fmt.Errorf("vectorbench: k %d exceeds corpus rows %d", c.K, c.Corpus.Rows)
	}
	return nil
}

// RunReport is the complete outcome of one benchmark run.
type RunReport struct {
	PgvectorVersion string
	Results         []Result
	Footprints      []Footprint
}

// loadChunkRows bounds one UNNEST insert; at 1024 dims a chunk is roughly
// 10 MB of text-form vectors, well under protocol limits.
const loadChunkRows = 1000

// warmupQueries are run untimed before each cell so the first measured query
// does not pay the buffer-cache fill.
const warmupQueries = 5

// Run executes the full benchmark: generate the corpus, build the schema and
// indexes, compute the exact ground truth, measure every cell, and collect
// object sizes. It owns the vectorbench schema and never touches application
// tables.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg Config, logger *slog.Logger) (*RunReport, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cells, err := ExpandMatrix(cfg.Matrix)
	if err != nil {
		return nil, err
	}
	version, err := pgvectorVersion(ctx, pool)
	if err != nil {
		return nil, err
	}
	iterative, err := supportsIterativeScan(version)
	if err != nil {
		return nil, err
	}
	logger.InfoContext(ctx, "vectorbench starting",
		slog.String("pgvector", version),
		slog.Int("rows", cfg.Corpus.Rows),
		slog.Int("queries", cfg.Corpus.Queries),
		slog.Int("dims", cfg.Corpus.Dims),
		slog.Int("cells", len(cells)))

	corpus, err := GenerateCorpus(cfg.Corpus)
	if err != nil {
		return nil, err
	}
	if !cfg.Keep {
		defer dropSchema(ctx, pool, logger)
	}
	if err := buildSchema(ctx, pool, cfg, logger); err != nil {
		return nil, err
	}
	if err := loadCorpus(ctx, pool, cfg.Corpus.Dims, corpus, logger); err != nil {
		return nil, err
	}
	if err := buildIndexes(ctx, pool, cfg, logger); err != nil {
		return nil, err
	}
	truth, err := exactBaselines(ctx, pool, cfg, corpus)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(cells))
	for _, cell := range cells {
		if cell.Iterative != IterativeOff && !iterative {
			results = append(results, Result{
				Cell:    cell,
				Skipped: fmt.Sprintf("pgvector %s lacks iterative scans", version),
			})
			continue
		}
		r, err := measureCell(ctx, pool, cfg, corpus, truth, cell)
		if err != nil {
			return nil, fmt.Errorf("measuring %s: %w", cell.Label(), err)
		}
		logger.InfoContext(ctx, "cell measured",
			slog.String("cell", cell.Label()),
			slog.Float64("recall", r.Recall),
			slog.Duration("p50", r.P50),
			slog.Duration("p95", r.P95))
		results = append(results, r)
	}
	footprints, err := collectFootprints(ctx, pool)
	if err != nil {
		return nil, err
	}
	return &RunReport{PgvectorVersion: version, Results: results, Footprints: footprints}, nil
}

func pgvectorVersion(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	if _, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return "", fmt.Errorf("creating vector extension: %w", err)
	}
	var version string
	err := pool.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname = 'vector'").Scan(&version)
	if err != nil {
		return "", fmt.Errorf("reading pgvector version: %w", err)
	}
	return version, nil
}

// supportsIterativeScan reports whether the pgvector version carries the
// hnsw.iterative_scan GUC, introduced in 0.8.0.
func supportsIterativeScan(version string) (bool, error) {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false, fmt.Errorf("vectorbench: malformed pgvector version %q", version)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false, fmt.Errorf("vectorbench: malformed pgvector version %q: %w", version, err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false, fmt.Errorf("vectorbench: malformed pgvector version %q: %w", version, err)
	}
	return major > 0 || minor >= 8, nil
}

func buildSchema(ctx context.Context, pool *pgxpool.Pool, cfg Config, logger *slog.Logger) error {
	sources := make([]string, cfg.Corpus.Sources)
	for i := range sources {
		sources[i] = SourceLabel(i)
	}
	ddl, err := SetupDDL(cfg.Corpus.Dims, sources)
	if err != nil {
		return err
	}
	for _, stmt := range ddl {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("schema DDL %q: %w", stmt, err)
		}
	}
	logger.InfoContext(ctx, "schema built", slog.Int("statements", len(ddl)))
	return nil
}

// loadCorpus bulk-loads the corpus into the single table with chunked UNNEST
// inserts carrying text-form vectors (the halfvec-safe wire form; binary COPY
// corrupts halfvec), then clones it server-side into the partitioned table.
func loadCorpus(ctx context.Context, pool *pgxpool.Pool, dims int, corpus Corpus, logger *slog.Logger) error {
	insert := fmt.Sprintf(
		"INSERT INTO %s (id, source, embedding) SELECT u.id, u.source, u.emb::halfvec(%d) FROM unnest($1::bigint[], $2::text[], $3::text[]) AS u(id, source, emb)",
		singleTable, dims,
	)
	start := time.Now()
	for offset := 0; offset < len(corpus.Vectors); offset += loadChunkRows {
		end := min(offset+loadChunkRows, len(corpus.Vectors))
		ids := make([]int64, 0, end-offset)
		sources := make([]string, 0, end-offset)
		vectors := make([]string, 0, end-offset)
		for i := offset; i < end; i++ {
			ids = append(ids, int64(i))
			sources = append(sources, corpus.Sources[i])
			vectors = append(vectors, VectorLiteral(corpus.Vectors[i]))
		}
		if _, err := pool.Exec(ctx, insert, ids, sources, vectors); err != nil {
			return fmt.Errorf("loading rows %d..%d: %w", offset, end, err)
		}
	}
	clone := fmt.Sprintf("INSERT INTO %s SELECT * FROM %s", partitionedTable, singleTable)
	if _, err := pool.Exec(ctx, clone); err != nil {
		return fmt.Errorf("cloning into partitioned table: %w", err)
	}
	logger.InfoContext(ctx, "corpus loaded",
		slog.Int("rows", len(corpus.Vectors)),
		slog.Duration("took", time.Since(start)))
	return nil
}

func buildIndexes(ctx context.Context, pool *pgxpool.Pool, cfg Config, logger *slog.Logger) error {
	for _, scenario := range cfg.Matrix.Scenarios {
		ddl, err := IndexDDL(scenario, cfg.Corpus.Dims)
		if err != nil {
			return err
		}
		start := time.Now()
		for _, stmt := range ddl {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("index DDL %q: %w", stmt, err)
			}
		}
		logger.InfoContext(ctx, "index built",
			slog.String("scenario", string(scenario)),
			slog.Duration("took", time.Since(start)))
	}
	return nil
}

// groundTruth holds the exact top-k ids per query, per filter.
type groundTruth map[Filter][][]int64

// sourceForQuery deterministically assigns the source a filtered query
// scopes to, cycling through the label set.
func sourceForQuery(i, sources int) string {
	return SourceLabel(i % sources)
}

// exactBaselines computes the ground truth with index scans disabled, so the
// planner evaluates exact distances over every row.
func exactBaselines(ctx context.Context, pool *pgxpool.Pool, cfg Config, corpus Corpus) (groundTruth, error) {
	truth := groundTruth{}
	for _, filter := range cfg.Matrix.Filters {
		sql, err := ExactSQL(filter, cfg.Corpus.Dims)
		if err != nil {
			return nil, err
		}
		perQuery := make([][]int64, len(corpus.Queries))
		err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL enable_indexscan = off"); err != nil {
				return fmt.Errorf("disabling index scans: %w", err)
			}
			for i, q := range corpus.Queries {
				args := []any{VectorLiteral(q), cfg.K}
				if filter == FilterSource {
					args = append(args, sourceForQuery(i, cfg.Corpus.Sources))
				}
				ids, err := queryIDs(ctx, tx, sql, args)
				if err != nil {
					return fmt.Errorf("exact query %d: %w", i, err)
				}
				perQuery[i] = ids
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("exact baseline filter=%s: %w", filter, err)
		}
		truth[filter] = perQuery
	}
	return truth, nil
}

// measureCell runs every query through one cell's configuration on a single
// pinned connection and scores recall and latency against the ground truth.
func measureCell(ctx context.Context, pool *pgxpool.Pool, cfg Config, corpus Corpus, truth groundTruth, cell Cell) (Result, error) {
	sql, err := SearchSQL(cell.Scenario, cell.Filter, cfg.Corpus.Dims)
	if err != nil {
		return Result{}, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET hnsw.ef_search = %d", cell.EfSearch)); err != nil {
		return Result{}, fmt.Errorf("setting ef_search: %w", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET hnsw.iterative_scan = %s", cell.Iterative)); err != nil {
		return Result{}, fmt.Errorf("setting iterative_scan: %w", err)
	}
	defer resetGUCs(ctx, conn)

	approx := make([][]int64, len(corpus.Queries))
	latencies := make([]time.Duration, 0, len(corpus.Queries))
	for i, q := range corpus.Queries {
		args := cellArgs(cfg, cell, q, i)
		if i < warmupQueries {
			if _, err := queryIDs(ctx, conn, sql, args); err != nil {
				return Result{}, fmt.Errorf("warmup query %d: %w", i, err)
			}
		}
		start := time.Now()
		ids, err := queryIDs(ctx, conn, sql, args)
		if err != nil {
			return Result{}, fmt.Errorf("query %d: %w", i, err)
		}
		latencies = append(latencies, time.Since(start))
		approx[i] = ids
	}
	recall, err := RecallAtK(truth[cell.Filter], approx)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Cell:   cell,
		Recall: recall,
		P50:    Percentile(latencies, 50),
		P95:    Percentile(latencies, 95),
	}, nil
}

// cellArgs assembles the positional arguments SearchSQL expects for one query.
func cellArgs(cfg Config, cell Cell, query []float32, i int) []any {
	args := []any{VectorLiteral(query), cfg.K}
	if cell.Scenario == ScenarioBQ {
		args = append(args, cell.Multiplier*cfg.K)
	}
	if cell.Filter == FilterSource {
		args = append(args, sourceForQuery(i, cfg.Corpus.Sources))
	}
	return args
}

// querier is the subset of pgx execution shared by pooled connections and
// transactions.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func queryIDs(ctx context.Context, q querier, sql string, args []any) ([]int64, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("collecting ids: %w", err)
	}
	return ids, nil
}

func resetGUCs(ctx context.Context, conn *pgxpool.Conn) {
	_, _ = conn.Exec(ctx, "RESET hnsw.ef_search")
	_, _ = conn.Exec(ctx, "RESET hnsw.iterative_scan")
}

// relation is one pg_class row in the vectorbench schema.
type relation struct {
	Name  string
	Kind  string
	Bytes int64
}

func collectFootprints(ctx context.Context, pool *pgxpool.Pool) ([]Footprint, error) {
	// Tables are measured with pg_table_size so the TOAST fork counts: a
	// halfvec(1024) attribute exceeds the TOAST threshold, so the vectors
	// live out of line and pg_relation_size alone would miss nearly all of
	// the data. Indexes have no TOAST; pg_relation_size is exact for them.
	rows, err := pool.Query(ctx, `
		SELECT c.relname, c.relkind::text,
		       CASE WHEN c.relkind = 'r' THEN pg_table_size(c.oid) ELSE pg_relation_size(c.oid) END
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'i')
		ORDER BY c.relname`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("listing relations: %w", err)
	}
	relations, err := pgx.CollectRows(rows, pgx.RowToStructByPos[relation])
	if err != nil {
		return nil, fmt.Errorf("collecting relations: %w", err)
	}
	if len(relations) == 0 {
		return nil, errors.New("vectorbench: no relations found in benchmark schema")
	}
	return classifyFootprints(relations), nil
}

// classifyFootprints aggregates raw relation sizes into the handful of named
// objects the verdict compares: the single table and its two indexes, and the
// partitioned layout's tables and indexes as totals.
func classifyFootprints(relations []relation) []Footprint {
	var single, singleIdx, bitIdx, partTables, partIdx int64
	var haveSingle, haveSingleIdx, haveBitIdx, havePart, havePartIdx bool
	for _, r := range relations {
		switch {
		case r.Name == "single_vectors":
			single, haveSingle = r.Bytes, true
		case r.Name == "single_vectors_embedding_hnsw":
			singleIdx, haveSingleIdx = r.Bytes, true
		case r.Name == "single_vectors_embedding_bit_hnsw":
			bitIdx, haveBitIdx = r.Bytes, true
		case strings.HasPrefix(r.Name, "part_vectors") && r.Kind == "r":
			partTables, havePart = partTables+r.Bytes, true
		case strings.HasPrefix(r.Name, "part_vectors") && r.Kind == "i":
			partIdx, havePartIdx = partIdx+r.Bytes, true
		}
	}
	footprints := make([]Footprint, 0, 5)
	add := func(have bool, name string, bytes int64) {
		if have {
			footprints = append(footprints, Footprint{Name: name, Bytes: bytes})
		}
	}
	add(haveSingle, "single table", single)
	add(haveSingleIdx, "single hnsw index (halfvec)", singleIdx)
	add(haveBitIdx, "bit expression index (binary quantize)", bitIdx)
	add(havePart, "partitioned tables (total)", partTables)
	add(havePartIdx, "partitioned hnsw indexes (total)", partIdx)
	return slices.Clip(footprints)
}

func dropSchema(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE"); err != nil {
		logger.ErrorContext(ctx, "dropping benchmark schema", slog.String("error", err.Error()))
	}
}
