locals {
  name = "${var.project}-${var.environment}"
}

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

# --- GitHub OIDC ---------------------------------------------------------
# The provider is account-global; create it once (dev) and reference it from
# other environments with create_oidc_provider = false.

resource "aws_iam_openid_connect_provider" "github" {
  count = var.create_oidc_provider ? 1 : 0

  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
}

data "aws_iam_openid_connect_provider" "github" {
  count = var.create_oidc_provider ? 0 : 1

  url = "https://token.actions.githubusercontent.com"
}

locals {
  oidc_provider_arn = var.create_oidc_provider ? aws_iam_openid_connect_provider.github[0].arn : data.aws_iam_openid_connect_provider.github[0].arn
}

# --- Deploy role (assumed by GitHub Actions) ------------------------------

data "aws_iam_policy_document" "deploy_trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [local.oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    # Pinned to an explicit allowlist of refs: PR branches and forks can never
    # assume the deploy role. StringLike (not StringEquals) so a bounded glob
    # like refs/tags/v* authorizes the tag-release path (release.yml) while a
    # literal ref like refs/heads/main still matches exactly. Never widen any
    # entry to refs/* — see github_deploy_refs.
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = [for r in var.github_deploy_refs : "repo:${var.github_repository}:ref:${r}"]
    }
  }
}

resource "aws_iam_role" "deploy" {
  name               = "${local.name}-deploy"
  assume_role_policy = data.aws_iam_policy_document.deploy_trust.json
}

data "aws_iam_policy_document" "deploy" {
  statement {
    sid       = "EcrAuth"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid = "EcrPush"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
      "ecr:InitiateLayerUpload",
      "ecr:UploadLayerPart",
      "ecr:CompleteLayerUpload",
      "ecr:PutImage",
    ]
    resources = var.ecr_repository_arns
  }

  statement {
    sid = "EcsDeploy"
    actions = [
      "ecs:DescribeServices",
      "ecs:UpdateService",
      "ecs:RunTask",
      "ecs:DescribeTasks",
      "ecs:DescribeTaskDefinition",
    ]
    resources = ["*"]

    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [var.cluster_arn]
    }
  }

  # DescribeTaskDefinition / RunTask validate against the task definition
  # family, which does not carry the cluster condition key.
  statement {
    sid = "EcsTaskDefinition"
    actions = [
      "ecs:DescribeTaskDefinition",
      "ecs:RunTask",
    ]
    resources = ["arn:aws:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:task-definition/${local.name}-*"]
  }

  # RegisterTaskDefinition re-pins a producer/worker family to the deployed image
  # (scripts/deploy-ingestion.sh) and rolls the queue message version. The action
  # operates on the account's task-definition namespace, not an individual
  # resource: AWS does not support resource-level ARNs or a family/cluster
  # condition key for RegisterTaskDefinition, so "*" is the only valid resource.
  # The follow-on RunTask/DescribeTaskDefinition that use the registered revision
  # stay scoped to the ${local.name}-* family above, bounding what the deploy
  # role can actually launch.
  statement {
    sid       = "EcsRegisterTaskDefinition"
    actions   = ["ecs:RegisterTaskDefinition"]
    resources = ["*"]
  }

  # InvokeFunction rolls the worker fleet through the worker-lifecycle deploy
  # lambda (scripts/deploy-ingestion.sh), draining in-flight work on the old task
  # set before retiring it. Scoped to this environment's lambda functions.
  statement {
    sid       = "InvokeWorkerLifecycle"
    actions   = ["lambda:InvokeFunction"]
    resources = ["arn:aws:lambda:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:function:${local.name}-*"]
  }

  statement {
    sid       = "PassTaskRoles"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.task_execution.arn, aws_iam_role.task.arn]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }

  dynamic "statement" {
    for_each = length(var.ssm_parameter_arns) > 0 ? [1] : []

    content {
      sid       = "ReadDeployParameters"
      actions   = ["ssm:GetParameter", "ssm:GetParameters"]
      resources = var.ssm_parameter_arns
    }
  }
}

resource "aws_iam_role_policy" "deploy" {
  name   = "deploy"
  role   = aws_iam_role.deploy.id
  policy = data.aws_iam_policy_document.deploy.json
}

# --- ECS task roles --------------------------------------------------------

data "aws_iam_policy_document" "ecs_tasks_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

# Execution role: pulls images, writes logs, injects secrets at task start.
resource "aws_iam_role" "task_execution" {
  name               = "${local.name}-task-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_trust.json
}

resource "aws_iam_role_policy_attachment" "task_execution_managed" {
  role       = aws_iam_role.task_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "task_execution_secrets" {
  statement {
    sid       = "InjectSecrets"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = var.secret_arns
  }
}

resource "aws_iam_role_policy" "task_execution_secrets" {
  name   = "secrets"
  role   = aws_iam_role.task_execution.id
  policy = data.aws_iam_policy_document.task_execution_secrets.json
}

# Task role: what the application itself may call. Empty until a feature
# needs AWS APIs; attach policies here, never to the execution role.
resource "aws_iam_role" "task" {
  name               = "${local.name}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_trust.json
}

# Least-privilege media-storage access: object-level read/write for presigned
# URLs, plus the bucket-level grants the SDK needs to address objects. Scoped
# to the one bucket and only attached when its ARN is supplied.
data "aws_iam_policy_document" "task_media" {
  count = var.media_bucket_arn == "" ? 0 : 1

  statement {
    sid       = "MediaObjects"
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${var.media_bucket_arn}/*"]
  }

  # ListBucket lets a HEAD on a missing key return 404 rather than 403, so the
  # storage layer can report absence instead of an access error.
  statement {
    sid       = "MediaBucket"
    actions   = ["s3:ListBucket"]
    resources = [var.media_bucket_arn]
  }
}

resource "aws_iam_role_policy" "task_media" {
  count  = var.media_bucket_arn == "" ? 0 : 1
  name   = "media-storage"
  role   = aws_iam_role.task.id
  policy = data.aws_iam_policy_document.task_media[0].json
}

# Least-privilege backup access: write-only on the dump bucket. The scheduled
# backup task assumes this same task role, so it may upload dumps but never read
# or delete an existing one. Scoped to the one bucket and only attached when its
# ARN is supplied.
data "aws_iam_policy_document" "task_db_backup" {
  count = var.db_backup_bucket_arn == "" ? 0 : 1

  statement {
    sid       = "PutBackupObjects"
    actions   = ["s3:PutObject"]
    resources = ["${var.db_backup_bucket_arn}/*"]
  }
}

resource "aws_iam_role_policy" "task_db_backup" {
  count  = var.db_backup_bucket_arn == "" ? 0 : 1
  name   = "db-backup"
  role   = aws_iam_role.task.id
  policy = data.aws_iam_policy_document.task_db_backup[0].json
}
