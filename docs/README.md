# Documentation

| Topic | Where |
|-------|-------|
| Layers, claim pipeline, surfaces, data, local services, configuration essentials | [`architecture.md`](architecture.md) |
| Tests, CI, and the Claude delivery workflow | [`development.md`](development.md) |
| Configuration, auth secrets, analysis cache, local seeded data | [`configuration.md`](configuration.md) |
| First production setup, from a clean AWS account to the first ingested data | [`first-setup.md`](first-setup.md) |
| Infrastructure and operations: AWS edge, RDS, Valkey, observability, human-gated deploys | [`infrastructure.md`](infrastructure.md) |
| Production Keycloak setup runbook | [`keycloak-prod-setup.md`](keycloak-prod-setup.md) |
| Ingestion pipeline: the Wikipedia evidence corpus, local and cloud, consistency | [`ingestion-pipeline.md`](ingestion-pipeline.md) |
| Ingestion hosts: the on-demand `/crawler` and `/consumer` EC2 runbook | [`ingestion-hosts.md`](ingestion-hosts.md) |
| Fact-check evidence sources and how each one produces a verdict | [`fact-check-sources.md`](fact-check-sources.md) |
| Embedding-model migration runbook | [`embedding-model-migration.md`](embedding-model-migration.md) |
| Datastore scale benchmark verdict | [`datastore-scale-benchmark.md`](datastore-scale-benchmark.md) |
| Vector-first defaults: local models decide the clear cases, the LLM the grey zones | [`vector-first-defaults.md`](vector-first-defaults.md) |
| Video pre-analysis: durable one-shot fact-check, analysed playback, claim timeline | [`video-preanalysis.md`](video-preanalysis.md) |
| Speaker naming: who the diarized voices are, signals, operator corrections | [`speaker-naming.md`](speaker-naming.md) |
| Recap: an analysed video as a report, with PDF export | [`recap.md`](recap.md) |
| PDF fact-check: upload, analysis, the Documents surface | [`pdf-fact-check.md`](pdf-fact-check.md) |
| Backoffice: the admin-only ingestion area and its access model | [`backoffice.md`](backoffice.md) |
| Live TV capture: channels, recorder, retention, legal posture | [`tv-live.md`](tv-live.md) |
| Design specs and implementation plans, one file per epic or card | [`superpowers/specs/`](superpowers/specs/), [`superpowers/plans/`](superpowers/plans/) |
| Terraform roots, SSO setup, CI/CD roles | [`../stack/terraform/README.md`](../stack/terraform/README.md) |
| Dashboard scripts and environment | [`../stack/frontend/README.md`](../stack/frontend/README.md) |
| Data dictionary (Postgres + pgvector) | [`../.claude/skills/data-map/SKILL.md`](../.claude/skills/data-map/SKILL.md) |
| README artwork | [`assets/`](assets/) |
