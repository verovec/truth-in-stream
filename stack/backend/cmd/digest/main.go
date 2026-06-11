// Command digest assembles a daily development digest from git history, Linear
// card activity, and GitHub pull requests, and either posts it to Slack (the
// default) or prints it. It runs independently of the API server, as a cron job
// or on demand.
//
//	digest              post the Block Kit digest to SLACK_DIGEST_WEBHOOK_URL
//	digest --terminal   print the full, untruncated report to stdout
//	digest --dry-run    print the Slack Block Kit JSON without posting
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	payload := buildCollector().Collect(ctx)

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
// collector degrades any missing source to a note in the report.
func buildCollector() *report.Collector {
	commits := report.NewGitCommitSource(".")

	var linear report.LinearSource
	if key := os.Getenv("LINEAR_API_KEY"); key != "" {
		linear = report.NewLinearClient(key, getenv("LINEAR_PROJECT", defaultProject))
	}

	github := report.NewGitHubClient(getenv("GITHUB_REPO", defaultRepo), os.Getenv("GITHUB_TOKEN"))

	return report.NewCollector(commits, linear, github)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
