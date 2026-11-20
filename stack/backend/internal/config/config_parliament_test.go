package config

import "testing"

func TestLoadParliament(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Parliament
		wantErr bool
	}{
		{
			name: "defaults applied, per-dataset state paths",
			env:  map[string]string{"PARLIAMENT_DATASET": "an-amendements"},
			want: Parliament{
				Dataset: "an-amendements", Legislature: "17",
				MarkerPath:   "/state/parliament-an-amendements-marker.json",
				ManifestPath: "/state/parliament-an-amendements-manifest.json",
			},
		},
		{
			name: "legislature, since-year and max-items overrides",
			env: map[string]string{
				"PARLIAMENT_DATASET": "senat-scrutins", "PARLIAMENT_LEGISLATURE": "16",
				"PARLIAMENT_SINCE_YEAR": "2023", "PARLIAMENT_MAX_ITEMS": "500",
			},
			want: Parliament{
				Dataset: "senat-scrutins", Legislature: "16", SinceYear: 2023, MaxItems: 500,
				MarkerPath:   "/state/parliament-senat-scrutins-marker.json",
				ManifestPath: "/state/parliament-senat-scrutins-manifest.json",
			},
		},
		{name: "missing dataset rejected", env: map[string]string{}, wantErr: true},
		{name: "non-numeric legislature rejected", env: map[string]string{"PARLIAMENT_DATASET": "an-amendements", "PARLIAMENT_LEGISLATURE": "XVII"}, wantErr: true},
		{name: "path-traversal legislature rejected", env: map[string]string{"PARLIAMENT_DATASET": "an-amendements", "PARLIAMENT_LEGISLATURE": "../17"}, wantErr: true},
		{name: "negative max items rejected", env: map[string]string{"PARLIAMENT_DATASET": "an-amendements", "PARLIAMENT_MAX_ITEMS": "-1"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadParliament()
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

func TestLoadEvidenceQueue(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "amqp://app:dev@rabbitmq:5672/")
	q, err := LoadEvidenceQueue()
	if err != nil {
		t.Fatalf("LoadEvidenceQueue: %v", err)
	}
	if q.Name != "evidence.chunks" {
		t.Fatalf("queue name = %q, want evidence.chunks", q.Name)
	}
	if got, want := q.VersionedName(), "evidence.chunks.v2"; got != want {
		t.Fatalf("VersionedName() = %q, want %q", got, want)
	}
}

func TestLoadEvidenceQueueRequiresBrokerURL(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "")
	if _, err := LoadEvidenceQueue(); err == nil {
		t.Fatal("LoadEvidenceQueue with no RABBITMQ_URL returned nil error, want error")
	}
}

func TestLoadEvidenceWorker(t *testing.T) {
	t.Setenv("EVIDENCE_WORKER_CONCURRENCY", "6")
	t.Setenv("EVIDENCE_WORKER_MAX_ATTEMPTS", "5")
	w, err := LoadEvidenceWorker()
	if err != nil {
		t.Fatalf("LoadEvidenceWorker: %v", err)
	}
	if w.Concurrency != 6 || w.MaxAttempts != 5 {
		t.Fatalf("worker = %+v, want Concurrency 6 / MaxAttempts 5", w)
	}
}
