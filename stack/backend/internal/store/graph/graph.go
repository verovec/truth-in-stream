// Package graph implements domain.GraphRepository on the embedded Ladybug engine.
// It owns the single process-wide READ_WRITE database handle.
package graph

import (
	"context"
	"fmt"

	lbug "github.com/LadybugDB/go-ladybug"
)

// Repository wraps the single Ladybug database handle for the process.
type Repository struct {
	db *lbug.Database
}

// Open opens (or creates) the Ladybug database at path.
func Open(path string) (*Repository, error) {
	db, err := lbug.OpenDatabase(path, lbug.DefaultSystemConfig())
	if err != nil {
		return nil, fmt.Errorf("graph: open %q: %w", path, err)
	}
	return &Repository{db: db}, nil
}

// Ping verifies the engine answers a trivial query.
func (r *Repository) Ping(ctx context.Context) error {
	conn, err := lbug.OpenConnection(r.db)
	if err != nil {
		return fmt.Errorf("graph: open connection: %w", err)
	}
	defer conn.Close()

	res, err := conn.Query("RETURN 1")
	if err != nil {
		return fmt.Errorf("graph: ping query: %w", err)
	}
	res.Close()
	return nil
}

// Close releases the database handle.
func (r *Repository) Close() error {
	r.db.Close()
	return nil
}
