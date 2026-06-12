# Terraform — truth-in-stream

Infrastructure as code, AWS region `eu-west-3`. Layout is directory-per-environment:
each of `dev/` and `prod/` is an independent root module with its own isolated state.

```
stack/terraform/
├── modules/
│   ├── vpc/             2-AZ VPC, public/private subnets, configurable NAT count, SGs
│   ├── ecs/             Fargate cluster (Container Insights) + log group
│   ├── ecr/             image repositories, scan-on-push, lifecycle policy
│   ├── iam/             GitHub OIDC provider, deploy role, ECS task roles
│   ├── rds/             PostgreSQL 17 (pgvector), credentials + DSN in Secrets Manager (optional, enable_rds)
│   ├── rabbitmq/        Amazon MQ for RabbitMQ broker + URL secret (embedding-job queue)
│   ├── alb/             public ALB; HTTP now, HTTPS once certificate_arn is set
│   ├── service/         task definition + target group + listener rule + service
│   ├── worker/          headless ECS worker service (embedding-worker fleet)
│   ├── migration/       one-shot Fargate task running golang-migrate
│   ├── scheduled-task/  EventBridge-scheduled one-shot Fargate task (wiki delta sync)
│   ├── s3/              media object storage bucket (presigned uploads)
│   └── s3-backup/       versioned, lifecycle-retained pg_dump backup bucket
├── dev/       dev root module  (state key: dev/terraform.tfstate)
└── prod/      prod root module (state key: prod/terraform.tfstate)
```

## Architecture

Backend (`:8080`) and frontend (`:3000`) run as Fargate services in private
subnets behind one public ALB. Path routing: `/api/*` and `/healthz` go to the
backend, everything else to the frontend. RDS PostgreSQL 17 (pgvector) lives in
private subnets, reachable only from the task security group. All credentials
flow through Secrets Manager; containers receive them as ECS secrets. CI
deploys via the GitHub OIDC deploy role: push images to ECR, run the migration
task, wait for exit 0, then force a new deployment of both services.

Dev: single NAT gateway, `db.t4g.micro`, no Multi-AZ, nothing protected.
Prod: per-AZ NAT, Multi-AZ RDS, deletion protection. Same modules, different
variables.

### Optional RDS (`enable_rds`)

The database is developed locally for now, so **dev provisions no RDS by
default** (`enable_rds = false`). With RDS off, the DB-dependent consumers gate
themselves off too — the migration task and the embedding-worker service are not
created, and the backend service is deployed without a `DATABASE_URL` secret — so
a fresh `terraform apply` has no dangling references. Prod defaults
`enable_rds = true`, so production is unchanged and the managed database is
provisioned. Flip `enable_rds = true` in dev (via `terraform.tfvars` or `-var`)
to bring the managed database online there.

## AWS SSO profile

All operator tooling targets the account through one AWS SSO profile, named
`truth-in-stream-dev`. Configure it once per machine:

```sh
aws configure sso --profile truth-in-stream-dev
#   SSO start URL : <your IAM Identity Center start URL>
#   SSO region    : eu-west-3
#   Account       : <dev account id>
#   Default region: eu-west-3
```

Then `aws sso login --profile truth-in-stream-dev` opens a session. The bootstrap
script keys off this profile (`AWS_PROFILE`, default `truth-in-stream-dev`) and
runs `aws sso login` for you if the session has expired.

## Remote state

State lives in the S3 bucket `truth-in-stream-tfstate` with native S3 locking
(`use_lockfile = true`, no DynamoDB table — the `dynamodb_table` backend argument
is deprecated). The bucket must exist before `init`.

### One-time bootstrap (run once, out of band)

```sh
./scripts/bootstrap-tfstate.sh
```

Idempotent and safe to re-run: it creates the state bucket only when missing,
then asserts versioning (required for native locking), default AES256 encryption,
and a full public-access block on every run. Override `STATE_BUCKET`,
`AWS_REGION`, or `AWS_PROFILE` via the environment if needed.

## First deploy of an environment

1. `cd dev && terraform init && terraform apply` — creates the runtime. With the
   default `enable_rds = false` no database is provisioned, and the migration
   task and embedding worker are not created; the backend/frontend services flap
   until images exist, which is expected. Set `enable_rds = true` first if you
   want the managed database and migration path in dev.
2. Set the GitHub repository secret `AWS_DEPLOY_ROLE_ARN` from the
   `deploy_role_arn` output.
3. Put values into the app secrets (containers are created empty on purpose;
   tasks cannot start without them):
   ```sh
   aws secretsmanager put-secret-value \
     --secret-id truth-in-stream/dev/app/embedding-api-key --secret-string '<key>'
   aws secretsmanager put-secret-value \
     --secret-id truth-in-stream/dev/app/transcription-api-key --secret-string '<key>'
   ```
4. Run the `deploy` workflow (push to main or dispatch). It pushes images, rolls
   the services, and — when `enable_rds = true` — applies migrations.
5. Open the `app_url` output.

## Adding HTTPS later

Create a hosted zone + ACM certificate, then set `certificate_arn` on the
`alb` module call. The HTTP listener switches to a 301 redirect and services
attach to the HTTPS listener automatically.

## Usage

Always run from inside an environment directory:

```sh
cd dev
terraform init
terraform plan
terraform apply
```

## CI

`.github/workflows/terraform.yml` runs fmt/validate/plan on PRs and plan+apply for
`dev` on merge to `main`. CI authenticates to AWS via GitHub OIDC; set the
`AWS_ROLE_ARN` repository secret to an IAM role whose trust policy is scoped to this
repo. The state-bucket bootstrap script is unit-tested with a stubbed `aws` CLI in
the `pr.yml` `bootstrap-script` job. The application deploy workflow uses the
separate, narrower `AWS_DEPLOY_ROLE_ARN` created by the `iam` module. See
`.github/workflows/_terraform.yml` and `deploy.yml`.
