// Command digest assembles a development digest from git history, Linear card
// activity, and GitHub pull requests, and either posts it to Slack (the default)
// or prints it. It runs independently of the API server, as a cron job or on
// demand. Without --epic it reports the daily window; with --epic it recaps a
// finished epic.
//
//	digest                  post the Block Kit digest to SLACK_DIGEST_WEBHOOK_URL
//	digest --terminal       print the full, untruncated report to stdout
//	digest --dry-run        print the Slack Block Kit JSON without posting
//	digest --epic VER-93    recap a finished epic instead of the daily window
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/digestsummary"
	"github.com/verovec/truth-in-stream/backend/internal/llm"
	"github.com/verovec/truth-in-stream/backend/internal/report"
)

const (
	defaultRepo    = "verovec/truth-in-stream"
	defaultProject = "Truth in Stream"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "digest:", err)
		os.Exit(1)
	}
}

func run() error {
	terminal := flag.Bool("terminal", false, "print the detailed report to stdout instead of posting to Slack")
	dryRun := flag.Bool("dry-run", false, "print the Slack Block Kit JSON without posting")
	epic := flag.String("epic", "", "recap a finished epic (e.g. VER-93) instead of the daily window")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	payload := buildCollector(*epic).Collect(ctx)

	switch {
	case *terminal:
		fmt.Print(report.TerminalRenderer{}.Render(payload))
		return nil
	case *dryRun:
		out, err := report.NewSlackRenderer("").RenderJSON(payload)
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	default:
		webhook := os.Getenv("SLACK_DIGEST_WEBHOOK_URL")
		if webhook == "" {
			return fmt.Errorf("SLACK_DIGEST_WEBHOOK_URL is not set; set it, or run with --terminal or --dry-run")
		}
		if err := report.NewSlackRenderer(webhook).Post(ctx, payload); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "digest: posted to Slack")
		return nil
	}
}

// buildCollector wires the data sources from the environment. The git source is
// always available; Linear is enabled only when LINEAR_API_KEY is set, and the
// card summarizer only when DIGEST_SUMMARY_API_KEY is set. The collector
// degrades any missing source to a note (and missing summaries to card titles).
// A non-empty epicID puts the collector in epic-recap mode.
func buildCollector(epicID string) *report.Collector {
	commits := report.NewGitCommitSource(".")

	var linear report.LinearSource
	if key := os.Getenv("LINEAR_API_KEY"); key != "" {
		linear = report.NewLinearClient(key, getenv("LINEAR_PROJECT", defaultProject))
	}

	github := report.NewGitHubClient(getenv("GITHUB_REPO", defaultRepo), os.Getenv("GITHUB_TOKEN"))

	opts := []report.CollectorOption{}
	if epicID != "" {
		opts = append(opts, report.WithEpic(epicID))
	}
	if summarizer := buildSummarizer(); summarizer != nil {
		opts = append(opts, report.WithSummarizer(summarizer))
	}

	return report.NewCollector(commits, linear, github, opts...)
}

// buildSummarizer wires the per-card summarizer, honoring the global
// LLM_PROVIDER like every other LLM-backed stage. Under DeepSeek (the default)
// the key is DEEPSEEK_API_KEY; under Anthropic it is DIGEST_SUMMARY_API_KEY;
// under Gemini it is GEMINI_API_KEY. With no key for the selected provider the
// summarizer is off and shipped cards fall back to their titles. A construction
// error degrades the same way rather than failing the digest.
func buildSummarizer() report.CardSummarizer {
	provider := strings.ToLower(strings.TrimSpace(getenv("LLM_PROVIDER", string(llm.ProviderDeepSeek))))
	anthropicKey := os.Getenv("DIGEST_SUMMARY_API_KEY")
	geminiKey := os.Getenv("GEMINI_API_KEY")
	deepseekKey := os.Getenv("DEEPSEEK_API_KEY")

	var keyPresent bool
	switch provider {
	case string(llm.ProviderGemini):
		keyPresent = geminiKey != ""
	case string(llm.ProviderDeepSeek):
		keyPresent = deepseekKey != ""
	default:
		keyPresent = anthropicKey != ""
	}
	if !keyPresent {
		return nil
	}

	summarizer, err := digestsummary.New(digestsummary.Config{
		Provider:       llm.ProviderName(provider),
		APIKey:         anthropicKey,
		GeminiAPIKey:   geminiKey,
		DeepSeekAPIKey: deepseekKey,
		Model:          os.Getenv("DIGEST_SUMMARY_MODEL"),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "digest: card summaries disabled:", err)
		return nil
	}
	return summarizer
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
