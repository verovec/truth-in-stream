package config

import "testing"

func TestLoadScrutinsArchive(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    ScrutinsArchive
		wantErr bool
	}{
		{
			name: "defaults applied",
			env:  map[string]string{},
			want: ScrutinsArchive{Legislature: "17", MarkerPath: "/state/scrutins-marker.json"},
		},
		{
			name: "legislature override",
			env:  map[string]string{"SCRUTINS_LEGISLATURE": "16"},
			want: ScrutinsArchive{Legislature: "16", MarkerPath: "/state/scrutins-marker.json"},
		},
		{
			name: "marker path override",
			env:  map[string]string{"SCRUTINS_MARKER_PATH": "/data/m.json"},
			want: ScrutinsArchive{Legislature: "17", MarkerPath: "/data/m.json"},
		},
		{
			name: "empty marker path disables skip",
			env:  map[string]string{"SCRUTINS_MARKER_PATH": ""},
			want: ScrutinsArchive{Legislature: "17", MarkerPath: ""},
		},
		{name: "non-numeric legislature rejected", env: map[string]string{"SCRUTINS_LEGISLATURE": "XVII"}, wantErr: true},
		{name: "zero legislature rejected", env: map[string]string{"SCRUTINS_LEGISLATURE": "0"}, wantErr: true},
		{name: "path-traversal legislature rejected", env: map[string]string{"SCRUTINS_LEGISLATURE": "../17"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadScrutinsArchive()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLoadScrutinsQueue(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "amqp://app:dev@rabbitmq:5672/")
	q, err := LoadScrutinsQueue()
	if err != nil {
		t.Fatalf("LoadScrutinsQueue: %v", err)
	}
	if q.Name != "scrutins.votes" {
		t.Fatalf("queue name = %q, want scrutins.votes", q.Name)
	}
	if got, want := q.VersionedName(), "scrutins.votes.v1"; got != want {
		t.Fatalf("VersionedName() = %q, want %q", got, want)
	}
}

func TestLoadScrutinsQueueRequiresBrokerURL(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "")
	if _, err := LoadScrutinsQueue(); err == nil {
		t.Fatal("LoadScrutinsQueue with no RABBITMQ_URL returned nil error, want error")
	}
}

func TestLoadScrutinsWorker(t *testing.T) {
	t.Setenv("SCRUTINS_WORKER_CONCURRENCY", "8")
	t.Setenv("SCRUTINS_WORKER_MAX_ATTEMPTS", "4")
	w, err := LoadScrutinsWorker()
	if err != nil {
		t.Fatalf("LoadScrutinsWorker: %v", err)
	}
	if w.Concurrency != 8 || w.MaxAttempts != 4 {
		t.Fatalf("worker = %+v, want Concurrency 8 / MaxAttempts 4", w)
	}
}
