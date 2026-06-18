# Infrastructure

Operator tooling targets AWS through one SSO profile (`truth-in-stream-dev`, region `eu-west-3`).
State lives in the S3 bucket `truth-in-stream-tfstate` (native S3 locking). Dev provisions no RDS by
default (`enable_rds = false`); the database is developed locally.

```bash
./scripts/bootstrap-tfstate.sh                          # once, before the first init
cd stack/terraform/dev && terraform init && terraform plan
```

See [`stack/terraform/README.md`](../stack/terraform/README.md) for the SSO setup, the CI/CD roles and
pre-apply IAM guard, and the `enable_rds` toggle.

- **Database backups** - the DB holds expensive-to-recompute embeddings, so it is dumped with
  `pg_dump -Fc` and restored without re-embedding (`halfvec` round-trips byte-for-byte). Manual:
  `make backup` / `make restore` (set `DB_BACKUP_BUCKET`). Scheduled: a Fargate cron task gated by
  `enable_db_backup`. See [`modules/scheduled-task`](../stack/terraform/modules/scheduled-task/README.md).
- **Secrets** - Terraform creates the secret containers; values are set out of band with
  `./scripts/secrets.sh dev` (no value ever passes through an argv, log, or chat). ECS consumes
  secrets by ARN, so a roll needs no task-definition re-pin.
- **Cloud ingestion** (producer Fargate task, worker fleet, versioned queue, SSM bastion drain) is
  documented in [`ingestion-pipeline.md`](ingestion-pipeline.md#10-cloud--production-pipeline).

Deploys stay human-gated: a production deploy is always a deliberate `workflow_dispatch` of
`deploy.yml`. See [Development -> CI](development.md#ci).
