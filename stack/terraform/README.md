# Terraform — truth-in-stream

Infrastructure as code, AWS region `eu-west-3`. Layout is directory-per-environment:
each of `dev/` and `prod/` is an independent root module with its own isolated state.

```
stack/terraform/
├── modules/   reusable modules, referenced as ../modules/<name>
├── dev/       dev root module  (state key: dev/terraform.tfstate)
└── prod/      prod root module (state key: prod/terraform.tfstate)
```

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
repo. See `.github/workflows/_terraform.yml`.
