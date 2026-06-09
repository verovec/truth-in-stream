package service

import (
	"context"
	"errors"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(ctx context.Context) error { return f.err }

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
			hc := NewHealthChecker(fakePinger{err: tc.repoErr})
			err := hc.Check(context.Background())
			if tc.wantErr != (err != nil) {
				t.Fatalf("Check() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
