# Infrastructure

Operator tooling targets AWS through one SSO profile, region `eu-west-3`, with directory-per-env
roots under `stack/terraform`. State lives in the S3 bucket `truth-in-stream-tfstate` (native S3
locking). Deploys stay human-gated.

```bash
./scripts/bootstrap-tfstate.sh                          # once, before the first init
cd stack/terraform/dev && terraform init && terraform plan
```

See [`stack/terraform/README.md`](../stack/terraform/README.md) for the SSO setup, the CI/CD roles and
pre-apply IAM guard, and the per-variable toggles.

## Environments

- **dev** (`stack/terraform/dev`) provisions no RDS by default (`enable_rds = false`); the database is
  developed locally with Docker Compose.
- **prod** (`stack/terraform/prod`) is the live environment and provisions the full edge, RDS, and
  observability described below. It is reachable at <https://jeminforme.fr>.

## Production edge

The app is served at <https://jeminforme.fr> (apex and `www`) through CloudFront in front of a private
ALB; the ALB has no public DNS name and is reachable only from CloudFront.

```
  Browser
     |  HTTPS (jeminforme.fr / www)
     v
  CloudFront  ---- WAF web ACL (managed rules + per-IP rate limit)
     |  VPC origin, HTTP, private subnets
     v
  Internal ALB  (security group admits only the CloudFront origin-facing prefix list)
     |
     v
  ECS services  (frontend, backend)
```

- **CloudFront** terminates TLS and forwards every request dynamically (no caching of the app or
  `/api/*`); the WAFv2 web ACL (scope `CLOUDFRONT`, provisioned in `us-east-1`) applies AWS managed
  rule groups plus a per-IP rate limit, and its decision logs redact the `Authorization` and `Cookie`
  headers.
- **ALB** is internal, lives in the private subnets, and its security group admits only CloudFront's
  origin-facing prefix list, so the load balancer is never reachable from the public internet.
- **TLS + DNS** the ACM certificate (`jeminforme.fr` + `www`, DNS-validated) is created in `us-east-1`
  for CloudFront. The authoritative hosted zone stays in a separate main account, so a dedicated
  `stack/terraform/main-account` root writes the ACM validation records and the apex/`www` alias
  records to CloudFront cross-account. It is applied by hand and excluded from CI:
  `make tf-main-account-plan` / `make tf-main-account-apply`.
- **Media** is served as direct presigned S3 GET URLs and is not fronted by CloudFront.
- **Keycloak** the production identity provider is operator-managed out of band at
  `https://login.jeminforme.fr` and is not provisioned by this terraform; see
  [Configuration -> Local Keycloak](configuration.md#local-keycloak-identity-provider).

## Production database

Prod provisions RDS PostgreSQL 17 with `pgvector` (`enable_rds = true`, `db.t4g.small`, single-AZ,
gp3 storage, automated backups and deletion protection). It is private to the VPC and reached by ECS
tasks over a security group; the `DATABASE_URL` is held in Secrets Manager and consumed by ARN.

The already-embedded local database is loaded into RDS once over an SSM tunnel through a bastion
(`enable_bastion`, kept off except during the load):

```bash
make db-tunnel ENV=prod        # SSM port-forward to the private RDS on 5432 (keep running)
make db-push    ENV=prod       # in a second shell: load the dump (vectors via text COPY, halfvec-safe)
```

- **Database backups** - the DB holds expensive-to-recompute embeddings, so it is dumped with
  `pg_dump -Fc` and restored without re-embedding (`halfvec` round-trips byte-for-byte). Manual:
  `make backup` / `make restore` (set `DB_BACKUP_BUCKET`). Scheduled: a Fargate cron task gated by
  `enable_db_backup`. See [`modules/scheduled-task`](../stack/terraform/modules/scheduled-task/README.md).
- **Secrets** - Terraform creates the secret containers; values are set out of band with
  `make push-secrets ENV=prod` (no value ever passes through an argv, log, or chat). ECS consumes
  secrets by ARN, so a roll needs no task-definition re-pin.

## Observability

Logs, alarms, and Slack alerting are provisioned for prod (`stack/terraform/modules/observability`).

- **Logs** - the backend logs structured JSON (`slog`) to stdout; ECS ships container logs to
  CloudWatch under `/ecs/truth-in-stream-<env>`, retained for `log_retention_days` (default 30). The
  WAF and the Slack-forwarder lambda log to their own groups under the same retention.
- **Alarms** - CloudWatch alarms cover ALB 5xx, per-service unhealthy hosts, backend/frontend
  running-task counts, RDS CPU and free storage, Amazon MQ CPU, and a WAF blocked-request spike. They
  publish to an SNS `alerts` topic (a second copy lives in `us-east-1` for the CloudFront-scoped WAF
  metrics). A `truth-in-stream-<env>-health` CloudWatch dashboard (created by default) summarizes the
  same signals.
- **Slack alerting** - SNS fans out to a Slack-forwarder lambda that reads the webhook URL from
  Secrets Manager and posts the alarm notifications to Slack. The ingestion crawlers post run
  start/finish/failure notices to the same Slack webhook (`SLACK_WEBHOOK_URL`); both are a silent
  no-op when it is unset.
- **Health checks** - `/healthz` is the only unauthenticated backend route and backs the ALB target
  health check; the frontend target group checks `/login`.

## Cloud ingestion

The producer Fargate task, worker fleet, versioned queue, and SSM bastion drain are documented in
[`ingestion-pipeline.md`](ingestion-pipeline.md#13-cloud--production-pipeline). The on-demand operator
controls (`make worker-up` / `worker-down` / `worker-status`, `make ingest-run`) scale the fleets to
zero when idle so there is no standing ingestion cost.

## Deploys (human-gated)

A production deploy is always a deliberate `workflow_dispatch` of a per-service deploy workflow
(`deploy-backend`, `deploy-frontend`, `deploy-workers`, `deploy-backup`). See
[Development -> CI](development.md#ci). No `terraform apply`, no deploy dispatch, and no main-account
apply happens without explicit human approval.
