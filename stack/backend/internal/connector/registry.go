package connector

import "fmt"

// descriptors is the registry: the one table of every ingestion source the fleet
// operates. Adding a source is one entry here (plus its producer package, its
// compose service, and - if DefaultCron is set - its builder in cmd/scheduler);
// no other central file is edited. The four production sources migrated off the
// hand-wired scheduler/host-script cases keep their exact crons, queues, and
// forwarded env, so behavior is unchanged. The copyable recipe a new connector
// follows lives in internal/example + cmd/examplecrawl (a compile-checked template
// deliberately kept out of this live registry, so no operator action can run it
// against a real environment); connector_test validates a test-only descriptor for
// it to prove the entry a real source adds here is well-formed.
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
		// sdmx is host-only (on-demand macro-stat ingest via the crawler host),
		// never on the local scheduler, so it has no DefaultCron - exactly like
		// stats. Its producer writes rows and publishes embedding jobs the existing
		// embedworker drains, so it reuses the embedding.jobs queue and worker. The
		// endpoints (ECB, OECD) are anonymous, so it declares no secrets.
		Name:       "sdmx",
		Producer:   "sdmxcrawl",
		Worker:     "embedworker",
		Queue:      "embedding.jobs",
		ForwardEnv: []string{"SDMX_SOURCES", "SDMX_START_PERIOD", "SDMX_END_PERIOD", "WIKI_ENQUEUE_BATCH_SIZE"},
	},
	{
		// ods is host-only (on-demand ingest), like stats: it fetches the
		// OpenDataSoft portals (DREES/DARES/URSSAF) and the SSMSI delinquency CSV
		// bases, upserts un-embedded passages, and publishes embedding jobs to the
		// shared embedding.jobs queue the embedworker already drains - no new
		// consumer, no cron, no secret (the sources are keyless open data).
		Name:       "ods",
		Producer:   "odsingest",
		Worker:     "embedworker",
		Queue:      "embedding.jobs",
		ForwardEnv: []string{"WIKI_ENQUEUE_BATCH_SIZE"},
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

// Validate checks the whole registry: every descriptor is individually valid, no
// two share a name, and no two share a normalized env prefix (which would collide
// on one SCHEDULE_<PREFIX>_* knob and silently enable or cron-override both). It is
// the invariant a startup or a registry test asserts, so a malformed, duplicated,
// or colliding entry is caught before it reaches the scheduler or the host scripts.
func Validate() error { return validateDescriptors(descriptors) }

// validateDescriptors is the registry invariant, split out so a test can exercise
// it on a constructed slice (e.g. a deliberate prefix collision) without mutating
// the package registry.
func validateDescriptors(ds []Descriptor) error {
	seenName := make(map[string]struct{}, len(ds))
	seenPrefix := make(map[string]string, len(ds))
	for _, d := range ds {
		if err := d.Validate(); err != nil {
			return err
		}
		if _, dup := seenName[d.Name]; dup {
			return fmt.Errorf("connector: duplicate source name %q", d.Name)
		}
		seenName[d.Name] = struct{}{}
		prefix := d.EnvPrefix()
		if other, dup := seenPrefix[prefix]; dup {
			return fmt.Errorf("connector: sources %q and %q both normalize to env prefix %q, colliding on SCHEDULE_%s_*", other, d.Name, prefix, prefix)
		}
		seenPrefix[prefix] = d.Name
	}
	return nil
}
