package mqmetrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseBrokerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    BrokerCreds
		wantErr bool
	}{
		{
			name: "amqps url drops the amqp port and keeps host plus credentials",
			raw:  "amqps://app:s3cr3t@b-abc123.mq.eu-west-3.amazonaws.com:5671/",
			want: BrokerCreds{Host: "b-abc123.mq.eu-west-3.amazonaws.com", Username: "app", Password: "s3cr3t"},
		},
		{
			name: "url without explicit port",
			raw:  "amqps://app:pw@host.example/",
			want: BrokerCreds{Host: "host.example", Username: "app", Password: "pw"},
		},
		{name: "missing credentials is an error", raw: "amqps://host:5671/", wantErr: true},
		{name: "missing host is an error", raw: "amqps://app:pw@", wantErr: true},
		{name: "unparseable url is an error", raw: "://not a url", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseBrokerURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseBrokerURL(%q) = %+v, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBrokerURL(%q) unexpected error: %v", tt.raw, err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ParseBrokerURL(%q) mismatch (-want +got):\n%s", tt.raw, diff)
			}
		})
	}
}

func TestClientFetchQueues(t *testing.T) {
	t.Parallel()

	const body = `[
		{"name":"embedding.jobs.v1","vhost":"/","messages":7,"consumers":2,
		 "message_stats":{"publish_details":{"rate":3.5}}},
		{"name":"idle.v1","vhost":"/","messages":0,"consumers":0}
	]`

	var gotUser, gotPass, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	client := &Client{
		HTTP:    srv.Client(),
		BaseURL: srv.URL,
		Creds:   BrokerCreds{Username: "app", Password: "pw"},
	}
	queues, err := client.FetchQueues(context.Background())
	if err != nil {
		t.Fatalf("FetchQueues() error: %v", err)
	}

	if gotPath != "/api/queues" {
		t.Errorf("requested path = %q, want /api/queues", gotPath)
	}
	if gotUser != "app" || gotPass != "pw" {
		t.Errorf("basic auth = %q:%q, want app:pw", gotUser, gotPass)
	}

	want := []APIQueue{
		{Name: "embedding.jobs.v1", Vhost: "/", Messages: 7, Consumers: 2, MessageStats: &MessageStats{PublishDetails: RateDetails{Rate: 3.5}}},
		{Name: "idle.v1", Vhost: "/", Messages: 0, Consumers: 0},
	}
	if diff := cmp.Diff(want, queues); diff != "" {
		t.Errorf("FetchQueues() mismatch (-want +got):\n%s", diff)
	}
}

func TestClientFetchQueuesNon200(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "access refused", http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := &Client{HTTP: srv.Client(), BaseURL: srv.URL, Creds: BrokerCreds{Username: "app", Password: "bad"}}
	if _, err := client.FetchQueues(context.Background()); err == nil {
		t.Fatal("FetchQueues() expected an error on a 401 response")
	}
}
