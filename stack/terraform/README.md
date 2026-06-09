# Terraform — truth-in-stream

Infrastructure as code, AWS region `eu-west-3`. Layout is directory-per-environment:
each of `dev/` and `prod/` is an independent root module with its own isolated state.

```
stack/terraform/
├── modules/
│   ├── vpc/        2-AZ VPC, public/private subnets, configurable NAT count, SGs
│   ├── ecs/        Fargate cluster (Container Insights) + log group
│   ├── ecr/        image repositories, scan-on-push, lifecycle policy
│   ├── iam/        GitHub OIDC provider, deploy role, ECS task roles
│   ├── rds/        PostgreSQL 17 (pgvector), credentials + DSN in Secrets Manager
│   ├── alb/        public ALB; HTTP now, HTTPS once certificate_arn is set
│   ├── service/    task definition + target group + listener rule + service
│   └── migration/  one-shot Fargate task running golang-migrate
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

## Remote state

State lives in the S3 bucket `truth-in-stream-tfstate` with native S3 locking
(`use_lockfile = true`, no DynamoDB table). The bucket must exist before `init`.

### One-time bootstrap (run once, out of band)

```sh
aws s3api create-bucket \
  --bucket truth-in-stream-tfstate \
  --region eu-west-3 \
  --create-bucket-configuration LocationConstraint=eu-west-3

# Versioning is REQUIRED for native S3 state locking.
aws s3api put-bucket-versioning \
  --bucket truth-in-stream-tfstate \
  --versioning-configuration Status=Enabled

aws s3api put-bucket-encryption \
  --bucket truth-in-stream-tfstate \
  --server-side-encryption-configuration \
    '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
```

## First deploy of an environment

1. `cd dev && terraform init && terraform apply` — creates the runtime.
   Services flap until images exist; that is expected.
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
4. Run the `deploy` workflow (push to main or dispatch). It pushes images,
   applies migrations, and rolls the services.
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
repo. The application deploy workflow uses the separate, narrower
`AWS_DEPLOY_ROLE_ARN` created by the `iam` module. See
`.github/workflows/_terraform.yml` and `deploy.yml`.
