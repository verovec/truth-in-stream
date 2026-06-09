package service

import (
	"context"
	"errors"
	"testing"
)

type fakeGraph struct{ err error }

func (f fakeGraph) Ping(ctx context.Context) error { return f.err }

func TestHealthCheckerCheck(t *testing.T) {
	tests := []struct {
		name    string
		repoErr error
		wantErr bool
	}{
		{name: "graph healthy", repoErr: nil, wantErr: false},
		{name: "graph down", repoErr: errors.New("dial fail"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hc := NewHealthChecker(fakeGraph{err: tc.repoErr})
			err := hc.Check(context.Background())
			if tc.wantErr != (err != nil) {
				t.Fatalf("Check() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
