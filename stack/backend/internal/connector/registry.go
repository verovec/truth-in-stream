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
		// FACTCHECK_QUERIES is now OPTIONAL (a broadened default topic rotation is
		// baked in), so it is NOT in RequiredEnv: the host validate_env must not abort
		// the crawler when it is unset, and the shipped compose injects it empty.
		Name:        "factcheck",
		DefaultCron: "0 4 * * *",
		Producer:    "factcheckcrawl",
		Worker:      "factcheckworker",
		Queue:       "factcheck.claims",
		ForwardEnv: []string{
			"FACTCHECK_QUERIES", "FACTCHECK_PUBLISHER_SITES", "FACTCHECK_LANGUAGE",
			"FACTCHECK_MAX_PAGES", "FACTCHECK_MAX_AGE_DAYS", "FACTCHECK_CHECKPOINT_PATH",
		},
		Secrets: []SecretRef{{EnvVar: "FACTCHECK_API_KEY", SecretSuffix: "app/factcheck-api-key"}},
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
	// The parliament sources all run the one parliamentcrawl producer selected by
	// PARLIAMENT_DATASET; each downloads a bulk dump, diffs it against a per-dataset
	// manifest, and publishes only new or changed records. The textual datasets emit
	// generic evidence jobs to the shared evidence queue (an-amendements sets NewQueue
	// because evidence.chunks is the first queue the evidence worker serves; the rest
	// reuse it); the Senat scrutins dataset emits chamber-aware scrutin jobs to the
	// existing scrutins queue. All are keyless (public Licence Ouverte / Senat open
	// data).
	{
		Name:        "an-amendements",
		DefaultCron: "0 5 * * *",
		Producer:    "parliamentcrawl",
		Worker:      "evidenceworker",
		Queue:       "evidence.chunks",
		RequiredEnv: []string{"PARLIAMENT_DATASET"},
		ForwardEnv:  []string{"PARLIAMENT_DATASET", "PARLIAMENT_LEGISLATURE", "PARLIAMENT_MAX_ITEMS"},
		NewQueue:    true,
	},
	{
		Name:        "an-questions",
		DefaultCron: "20 5 * * *",
		Producer:    "parliamentcrawl",
		Worker:      "evidenceworker",
		Queue:       "evidence.chunks",
		RequiredEnv: []string{"PARLIAMENT_DATASET"},
		ForwardEnv:  []string{"PARLIAMENT_DATASET", "PARLIAMENT_LEGISLATURE", "PARLIAMENT_MAX_ITEMS"},
	},
	{
		Name:        "an-comptesrendus",
		DefaultCron: "40 5 * * *",
		Producer:    "parliamentcrawl",
		Worker:      "evidenceworker",
		Queue:       "evidence.chunks",
		RequiredEnv: []string{"PARLIAMENT_DATASET"},
		ForwardEnv:  []string{"PARLIAMENT_DATASET", "PARLIAMENT_LEGISLATURE", "PARLIAMENT_MAX_ITEMS"},
	},
	{
		Name:        "senat-questions",
		DefaultCron: "0 6 * * *",
		Producer:    "parliamentcrawl",
		Worker:      "evidenceworker",
		Queue:       "evidence.chunks",
		RequiredEnv: []string{"PARLIAMENT_DATASET"},
		ForwardEnv:  []string{"PARLIAMENT_DATASET", "PARLIAMENT_MAX_ITEMS"},
	},
	{
		Name:        "senat-dosleg",
		DefaultCron: "30 6 * * *",
		Producer:    "parliamentcrawl",
		Worker:      "evidenceworker",
		Queue:       "evidence.chunks",
		RequiredEnv: []string{"PARLIAMENT_DATASET"},
		ForwardEnv:  []string{"PARLIAMENT_DATASET", "PARLIAMENT_MAX_ITEMS"},
	},
	{
		Name:        "senat-scrutins",
		DefaultCron: "0 7 * * *",
		Producer:    "parliamentcrawl",
		Worker:      "scrutinsworker",
		Queue:       "scrutins.votes",
		RequiredEnv: []string{"PARLIAMENT_DATASET"},
		ForwardEnv:  []string{"PARLIAMENT_DATASET", "PARLIAMENT_SINCE_YEAR", "PARLIAMENT_MAX_ITEMS"},
	},
	{
		// datacommons is the keyless, redundant claim-corpus path: it reads the
		// DataCommons ClaimReview feed and publishes claim jobs to the same
		// factcheck.claims queue the Google-API factcheck source uses, so the
		// existing factcheckworker drains it (NewQueue stays false, no new worker).
		// The feed is a public object, so it declares no Secrets and needs no
		// per-source Terraform. Cron 05:00 UTC runs it after factcheck (04:00) and
		// scrutins (04:30) and off the Monday 04:00 broker-maintenance slot.
		Name:        "datacommons",
		DefaultCron: "0 5 * * *",
		Producer:    "datacommonscrawl",
		Worker:      "factcheckworker",
		Queue:       "factcheck.claims",
		ForwardEnv:  []string{"DATACOMMONS_FEED_URL", "DATACOMMONS_OUTLET_ALLOWLIST", "DATACOMMONS_MAX_ITEMS", "DATACOMMONS_FEED_FORMAT"},
	},
	{
		// claimreview reads ClaimReview JSON-LD directly from the allowlisted French
		// outlets (sitemap-discovered, robots- and pacing-respecting) and publishes to
		// the same factcheck.claims queue the existing factcheckworker drains. Keyless
		// (public outlet pages), so no Secrets and no per-source Terraform. Cron 05:30
		// UTC runs it after the factcheck/datacommons streams and off the Monday 04:00
		// broker-maintenance slot.
		Name:        "claimreview",
		DefaultCron: "30 5 * * *",
		Producer:    "claimreviewcrawl",
		Worker:      "factcheckworker",
		Queue:       "factcheck.claims",
		ForwardEnv:  []string{"CLAIMREVIEW_USER_AGENT", "CLAIMREVIEW_MIN_DELAY_MS", "CLAIMREVIEW_MAX_URLS"},
	},
	{
		// claimskg is the one-shot historical seed: it imports a ClaimsKG CSV/TSV
		// export into political_claims via the same factcheck.claims queue. It is
		// host-only (no DefaultCron): it never runs on the scheduler and is armed only
		// by CLAIMSKG_SEED_ENABLED + CLAIMSKG_SEED_FILE, so the large 2023 snapshot is
		// ingested only on a deliberate operator run. Keyless (a local export file).
		Name:       "claimskg",
		Producer:   "claimskgseed",
		Worker:     "factcheckworker",
		Queue:      "factcheck.claims",
		ForwardEnv: []string{"CLAIMSKG_SEED_ENABLED", "CLAIMSKG_SEED_FILE", "CLAIMSKG_SEED_VINTAGE", "CLAIMSKG_SEED_TSV"},
	},
	// The Phase-2 institutional evidence sources publish generic evidence jobs to the
	// shared evidence queue the evidence worker already drains. vie-publique and
	// hatvp are keyless open data run on the always-on scheduler; legifrance is a
	// host-only (on-demand), credential-gated source: its PISTE OAuth2 credentials are
	// declared as secrets read from Secrets Manager on the host, never forwarded, and
	// it degrades to a clean skip when they are absent.
	{
		Name:        "viepublique",
		DefaultCron: "0 8 * * *",
		Producer:    "viepubliquecrawl",
		Worker:      "evidenceworker",
		Queue:       "evidence.chunks",
		ForwardEnv:  []string{"VIEPUBLIQUE_MAX_ITEMS"},
	},
	{
		Name:        "hatvp",
		DefaultCron: "30 8 * * *",
		Producer:    "hatvpcrawl",
		Worker:      "evidenceworker",
		Queue:       "evidence.chunks",
		ForwardEnv:  []string{"HATVP_MAX_ITEMS"},
	},
	{
		Name:       "legifrance",
		Producer:   "legifrancecrawl",
		Worker:     "evidenceworker",
		Queue:      "evidence.chunks",
		ForwardEnv: []string{"LEGIFRANCE_ARTICLES", "LEGIFRANCE_MAX_ITEMS", "LEGIFRANCE_MIN_INTERVAL_MS", "LEGIFRANCE_TOKEN_URL", "LEGIFRANCE_API_BASE_URL"},
		Secrets: []SecretRef{
			{EnvVar: "LEGIFRANCE_CLIENT_ID", SecretSuffix: "app/legifrance-client-id"},
			{EnvVar: "LEGIFRANCE_CLIENT_SECRET", SecretSuffix: "app/legifrance-client-secret"},
		},
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
