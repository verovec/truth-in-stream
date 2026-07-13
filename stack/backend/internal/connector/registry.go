package connector

import "fmt"

// descriptors is the registry: the one table of every ingestion source the fleet
// operates. Adding a source is one entry here (plus its producer package, its
// compose service, and - if DefaultCron is set - its builder in cmd/scheduler);
// no other central file is edited. The four production sources migrated off the
// hand-wired scheduler/host-script cases keep their exact crons, queues, and
// forwarded env, so behavior is unchanged. The "example" entry is the in-tree
// template a new connector copies.
var descriptors = []Descriptor{
	{
		Name:        "wikipedia",
		DefaultCron: "0 3 * * *",
		Producer:    "wikicrawl",
		Worker:      "crawlworker",
		Queue:       "crawl.chunks",
		RequiredEnv: []string{"CRAWL_CATEGORIES"},
		ForwardEnv: []string{
			"CRAWL_CATEGORIES", "CRAWL_PROJECT", "WIKI_CORPUS", "CRAWL_CORPUS",
			"CRAWL_MAX_DEPTH", "CRAWL_MAX_PAGES", "CRAWL_INCLUDE_BODY",
			"CRAWL_SHARDS", "CRAWL_SHARD_INDEX", "CRAWL_CHECKWORTHY",
			"CRAWL_CHECKWORTHY_MODEL", "CRAWL_CHECKWORTHY_CONCURRENCY",
			"CRAWL_CHECKWORTHY_RPM", "LLM_PROVIDER",
		},
		Secrets: []SecretRef{{EnvVar: "CHECKWORTHY_API_KEY", SecretSuffix: "app/checkworthy-api-key"}},
	},
	{
		// stats is host-only (on-demand ingest), never on the local scheduler, so
		// it has no DefaultCron and no SCHEDULE_STATS_* knobs - exactly as before.
		Name:       "stats",
		Producer:   "statsingest",
		Worker:     "embedworker",
		Queue:      "embedding.jobs",
		ForwardEnv: []string{"WIKI_ENQUEUE_BATCH_SIZE"},
	},
	{
		Name:        "factcheck",
		DefaultCron: "0 4 * * *",
		Producer:    "factcheckcrawl",
		Worker:      "factcheckworker",
		Queue:       "factcheck.claims",
		RequiredEnv: []string{"FACTCHECK_QUERIES"},
		ForwardEnv:  []string{"FACTCHECK_QUERIES", "FACTCHECK_LANGUAGE", "FACTCHECK_MAX_PAGES"},
		Secrets:     []SecretRef{{EnvVar: "FACTCHECK_API_KEY", SecretSuffix: "app/factcheck-api-key"}},
	},
	{
		Name:        "scrutins",
		DefaultCron: "30 4 * * *",
		Producer:    "scrutinscrawl",
		Worker:      "scrutinsworker",
		Queue:       "scrutins.votes",
		ForwardEnv:  []string{"SCRUTINS_LEGISLATURE"},
	},
	{
		// example is the in-tree recipe template: a self-contained producer package
		// (internal/example) plus this one entry. It reuses the wikipedia job shape,
		// queue, and worker (crawl.chunks / crawlworker), so it needs no new
		// consumer. It defaults DISABLED like every source and is safe to keep in
		// the tree: it never runs until an operator opts it in.
		Name:        "example",
		DefaultCron: "0 5 * * *",
		Producer:    "examplecrawl",
		Worker:      "crawlworker",
		Queue:       "crawl.chunks",
		RequiredEnv: []string{"EXAMPLE_LABEL"},
		ForwardEnv:  []string{"EXAMPLE_LABEL", "EXAMPLE_MAX_ITEMS"},
	},
}

// All returns a copy of the registry in declaration order, so a caller cannot
// mutate the shared table. The scheduler iterates it to build its cron registry
// and the manifest is marshaled from it.
func All() []Descriptor {
	out := make([]Descriptor, len(descriptors))
	copy(out, descriptors)
	return out
}

// Names returns every registered source name in declaration order.
func Names() []string {
	names := make([]string, len(descriptors))
	for i, d := range descriptors {
		names[i] = d.Name
	}
	return names
}

// Lookup returns the descriptor for name and whether it is registered.
func Lookup(name string) (Descriptor, bool) {
	for _, d := range descriptors {
		if d.Name == name {
			return d, true
		}
	}
	return Descriptor{}, false
}

// Validate checks the whole registry: every descriptor is individually valid and
// no two share a name. It is the invariant a startup or a registry test asserts,
// so a malformed or duplicated entry is caught before it reaches the scheduler or
// the host scripts.
func Validate() error {
	seen := make(map[string]struct{}, len(descriptors))
	for _, d := range descriptors {
		if err := d.Validate(); err != nil {
			return err
		}
		if _, dup := seen[d.Name]; dup {
			return fmt.Errorf("connector: duplicate source name %q", d.Name)
		}
		seen[d.Name] = struct{}{}
	}
	return nil
}
