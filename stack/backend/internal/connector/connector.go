// Package connector is the ingestion fleet's source-adapter registry: the one
// place that declares every trusted data source the crawler/consumer fleet
// operates, so adding a source is a self-contained producer package plus one
// registry entry rather than hand-edited wiring scattered across the scheduler,
// the compose files, and the EC2 host scripts.
//
// # What a source is
//
// A [Descriptor] is the pure-data declaration of one source: its name, its
// optional local-scheduler cadence, the compose services that run its producer
// and worker, the broker queue it publishes to, the non-secret producer env the
// operator forwards, and the secrets the host materializes from Secrets Manager.
// The descriptor carries no behavior and no heavy imports (only [domain] and a
// cron parser), so both the Go scheduler and the shell host tooling read the same
// declaration: the scheduler iterates [All] to build its cron registry, and
// scripts/ingest-host.sh reads the [Manifest] JSON to resolve a source's
// producer, worker, queue, and forwarded env.
//
// # The generic evidence job
//
// [EvidenceJob] is the one job shape a connector that targets the evidence_chunks
// corpus emits: the self-contained (source, external_id, chunk_index, title, url,
// content, kind, metadata) tuple the embedding pipeline needs, with no per-source
// columns. Its [EvidenceJob.Chunk] maps straight onto [domain.EvidenceChunk], so
// any connector's output flows into the corpus through the same store write the
// wiki crawl worker already uses. A source with source-specific provenance puts
// it in Metadata (verbatim jsonb) instead of adding a column.
//
// # Adding a source (the recipe)
//
// internal/example + cmd/examplecrawl are the copyable, compile-checked template
// (kept out of the live registry on purpose). To add a real source:
//
//  1. Write the producer package under internal/<name> and its one-shot binary
//     under cmd/<name>crawl, exactly like the existing sources. If it targets the
//     evidence corpus, publish [EvidenceJob] bodies; if it reuses an existing job
//     shape (a Wikipedia chunk, a curated claim, a scrutin), publish that shape so
//     the existing worker and queue drain it with no new consumer.
//  2. Add one [Descriptor] to the slice in registry.go. Set DefaultCron only if
//     the source should run on the always-on local scheduler; leave it empty for a
//     host-only, on-demand source. Declare Producer/Worker/Queue to match the
//     compose services, list the non-secret producer env in ForwardEnv (and the
//     subset that must be set in RequiredEnv), and declare each API key in Secrets
//     - never in ForwardEnv, so a secret is only ever read from Secrets Manager on
//     the host (scripts/ingest-fetch-env.sh reads the declared secrets from the
//     manifest) and never travels through the operator's SSM command.
//  3. Regenerate the manifest (go test ./internal/connector -run Manifest -update,
//     or copy [MarshalManifest]'s output to sources.json) so the host scripts pick
//     the source up. Add the producer's compose service to
//     docker-compose.ingest.yml (and a worker service only when NewQueue is set).
//  4. If DefaultCron is set, add the builder that constructs the producer to the
//     builders table in cmd/scheduler - the one tiny wiring entry the framework
//     cannot derive, because only the cmd layer may import the broker and config.
//  5. For a secret-bearing source, create the Secrets Manager secret and push its
//     value with scripts/push-secrets.sh, AND add its ARN to the crawler (or
//     consumer) host's secret_arns in stack/terraform/dev/main.tf - the
//     ingestion-host module grants exactly the listed ARNs and rejects wildcards,
//     so a new secret ARN is the one required per-source Terraform edit. A keyless
//     source needs no Terraform change; the ingestion-host module stays generic.
package connector
