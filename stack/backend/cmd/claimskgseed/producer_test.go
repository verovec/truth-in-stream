package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/claimskg"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

type nopPublisher struct{ n int }

func (p *nopPublisher) Publish(context.Context, []byte, uint8) error { p.n++; return nil }

func TestClaimskgProducerReadsSeedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "seed.csv")
	csvData := "claimReviewed,rating,claimReview_url\n" +
		"\"Une affirmation\",\"Faux\",\"https://factuel.afp.com/x\"\n" +
		"\"Une autre\",\"Vrai\",\"https://www.lemonde.fr/y\"\n"
	if err := os.WriteFile(path, []byte(csvData), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	seeder, err := claimskg.New(claimskg.Config{Enabled: true, MaxPriority: 9})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pub := &nopPublisher{}
	p := claimskgProducer{seeder: seeder, pub: pub, seedFile: path, vintage: "2023"}

	if p.Name() != "claimskg" {
		t.Errorf("Name() = %q", p.Name())
	}
	stats, err := p.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New != 2 {
		t.Fatalf("stats.New = %d, want 2", stats.New)
	}
	if pub.n != 2 {
		t.Fatalf("published %d, want 2", pub.n)
	}
}

func TestClaimskgProducerMissingFileErrors(t *testing.T) {
	t.Parallel()
	seeder, _ := claimskg.New(claimskg.Config{Enabled: true, MaxPriority: 9})
	p := claimskgProducer{seeder: seeder, pub: &nopPublisher{}, seedFile: "/no/such/file.csv", vintage: "2023"}
	if _, err := p.Run(t.Context()); err == nil {
		t.Fatal("expected an error opening a missing seed file")
	}
}

var _ crawlnotify.Producer = claimskgProducer{}
