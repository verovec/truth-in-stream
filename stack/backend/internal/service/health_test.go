package service

import (
	"context"
	"errors"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type fakeStore struct{ err error }

func (f fakeStore) Ping(ctx context.Context) error                        { return f.err }
func (f fakeStore) Upsert(ctx context.Context, _ []domain.Document) error { return nil }
func (f fakeStore) Search(ctx context.Context, _ []float32, _ int) ([]domain.Match, error) {
	return nil, nil
}

func TestHealthCheckerCheck(t *testing.T) {
	tests := []struct {
		name    string
		repoErr error
		wantErr bool
	}{
		{name: "store healthy", repoErr: nil, wantErr: false},
		{name: "store down", repoErr: errors.New("dial fail"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hc := NewHealthChecker(fakeStore{err: tc.repoErr})
			err := hc.Check(context.Background())
			if tc.wantErr != (err != nil) {
				t.Fatalf("Check() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
