package vectorbench

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The harness owns its own schema so it can never collide with application
// tables, and a teardown is one DROP SCHEMA. SQL here is built as strings on
// purpose: the whole point of the harness is to generate DDL and queries
// across dimensions, index types, and partition layouts that sqlc cannot
// parameterize. Nothing in this package runs in the serving path.
const (
	schemaName       = "vectorbench"
	singleTable      = schemaName + ".single_vectors"
	partitionedTable = schemaName + ".part_vectors"
)

// sourceLabelPattern keeps interpolated source labels identifier-safe; labels
// come from SourceLabel but are validated again at the SQL boundary.
var sourceLabelPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// SetupDDL returns the statements that build the benchmark schema for dims:
// a fresh schema, the single (unpartitioned) table, and the same table
// partitioned by LIST (source) with one partition per source label.
func SetupDDL(dims int, sources []string) ([]string, error) {
	if dims <= 0 {
		return nil, fmt.Errorf("vectorbench: dims must be positive, got %d", dims)
	}
	if len(sources) == 0 {
		return nil, errors.New("vectorbench: no sources for partition layout")
	}
	for _, s := range sources {
		if !sourceLabelPattern.MatchString(s) {
			return nil, fmt.Errorf("vectorbench: source label %q is not identifier-safe", s)
		}
	}
	columns := fmt.Sprintf("(id bigint NOT NULL, source text NOT NULL, embedding halfvec(%d) NOT NULL)", dims)
	ddl := []string{
		"DROP SCHEMA IF EXISTS " + schemaName + " CASCADE",
		"CREATE SCHEMA " + schemaName,
		"CREATE TABLE " + singleTable + " " + columns,
		"CREATE TABLE " + partitionedTable + " " + columns + " PARTITION BY LIST (source)",
	}
	for _, s := range sources {
		ddl = append(ddl, fmt.Sprintf(
			"CREATE TABLE %s_%s PARTITION OF %s FOR VALUES IN ('%s')",
			partitionedTable, s, partitionedTable, s,
		))
	}
	return ddl, nil
}

// IndexDDL returns the index statements a scenario needs. HNSW build
// parameters match production (m=16, ef_construction=200) so measured recall
// and latency transfer. The partitioned parent index propagates one HNSW
// index per partition.
func IndexDDL(scenario Scenario, dims int) ([]string, error) {
	if dims <= 0 {
		return nil, fmt.Errorf("vectorbench: dims must be positive, got %d", dims)
	}
	const params = " WITH (m = 16, ef_construction = 200)"
	switch scenario {
	case ScenarioHNSW:
		return []string{
			"CREATE INDEX single_vectors_embedding_hnsw ON " + singleTable +
				" USING hnsw (embedding halfvec_cosine_ops)" + params,
		}, nil
	case ScenarioBQ:
		return []string{
			fmt.Sprintf("CREATE INDEX single_vectors_embedding_bit_hnsw ON %s USING hnsw ((binary_quantize(embedding)::bit(%d)) bit_hamming_ops)%s",
				singleTable, dims, params),
		}, nil
	case ScenarioPartitioned:
		return []string{
			"CREATE INDEX part_vectors_embedding_hnsw ON " + partitionedTable +
				" USING hnsw (embedding halfvec_cosine_ops)" + params,
		}, nil
	default:
		return nil, fmt.Errorf("vectorbench: unknown scenario %q", scenario)
	}
}

// SearchSQL returns the query for one scenario and filter. Parameters are
// $1 = query vector literal, $2 = k; the bq-rerank coarse limit is $3; the
// source filter binds as $3 (plain scans) or $4 (bq-rerank). The bq-rerank
// CTE is MATERIALIZED so the planner cannot collapse the coarse Hamming pass
// into the rerank and defeat the two-stage design.
func SearchSQL(scenario Scenario, filter Filter, dims int) (string, error) {
	if dims <= 0 {
		return "", fmt.Errorf("vectorbench: dims must be positive, got %d", dims)
	}
	vec := fmt.Sprintf("$1::halfvec(%d)", dims)
	switch scenario {
	case ScenarioHNSW, ScenarioPartitioned:
		table := singleTable
		if scenario == ScenarioPartitioned {
			table = partitionedTable
		}
		where := ""
		if filter == FilterSource {
			where = " WHERE source = $3"
		}
		return fmt.Sprintf("SELECT id FROM %s%s ORDER BY embedding <=> %s LIMIT $2", table, where, vec), nil
	case ScenarioBQ:
		where := ""
		if filter == FilterSource {
			where = " WHERE source = $4"
		}
		coarse := fmt.Sprintf("SELECT id, embedding FROM %s%s ORDER BY binary_quantize(embedding)::bit(%d) <~> binary_quantize(%s) LIMIT $3",
			singleTable, where, dims, vec)
		return fmt.Sprintf("WITH candidates AS MATERIALIZED (%s) SELECT id FROM candidates ORDER BY embedding <=> %s LIMIT $2",
			coarse, vec), nil
	default:
		return "", fmt.Errorf("vectorbench: unknown scenario %q", scenario)
	}
}

// ExactSQL returns the ground-truth query: the same single-table scan the
// HNSW scenario runs, executed by the caller with index scans disabled so the
// planner computes exact distances over every row.
func ExactSQL(filter Filter, dims int) (string, error) {
	return SearchSQL(ScenarioHNSW, filter, dims)
}

// VectorLiteral renders v in pgvector's text format, the halfvec-safe wire
// form for both COPY and query parameters (binary encoding corrupts halfvec).
func VectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v)*10 + 2)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
