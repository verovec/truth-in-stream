# Apply-time IAM actions the CI apply role (AWS_ROLE_ARN) must hold to provision
# this environment. This is the single source of truth the pre-apply guard
# (scripts/iam-apply-guard.sh) checks the role against before `terraform apply`.
#
# Why this exists: the apply role cannot grant itself permissions it lacks, so
# the first apply that introduces a new resource type would fail halfway. By
# declaring the required actions here, the guard detects that case up front and
# tells the operator to run that one apply manually with elevated credentials.
#
# Maintenance contract: when a later card adds a resource area, it appends that
# area's actions to the matching block below in the same change - so the required
# permissions never drift from what terraform provisions. Each block is scoped to
# one concern. List concrete actions only; never "*". Resource-level scoping is
# enforced on the role's own policy; this manifest is the action contract used to
# catch the chicken-and-egg case.

locals {
  # S3 remote state with native locking (read/write the state object and the
  # lock file). Always required.
  state_actions = [
    "s3:GetObject",
    "s3:PutObject",
    "s3:DeleteObject",
    "s3:ListBucket",
    "s3:GetBucketVersioning",
  ]

  # The guard runs as the apply role and simulates the role's own policy.
  guard_actions = [
    "iam:SimulatePrincipalPolicy",
    "iam:GetRole",
  ]

  # Networking (modules/vpc): VPC, subnets, route tables, NAT, IGW, SGs, the S3
  # gateway endpoint.
  networking_actions = [
    "ec2:CreateVpc",
    "ec2:DeleteVpc",
    "ec2:ModifyVpcAttribute",
    "ec2:CreateSubnet",
    "ec2:DeleteSubnet",
    "ec2:CreateRouteTable",
    "ec2:DeleteRouteTable",
    "ec2:CreateRoute",
    "ec2:AssociateRouteTable",
    "ec2:CreateNatGateway",
    "ec2:DeleteNatGateway",
    "ec2:CreateInternetGateway",
    "ec2:AttachInternetGateway",
    "ec2:AllocateAddress",
    "ec2:ReleaseAddress",
    "ec2:CreateSecurityGroup",
    "ec2:DeleteSecurityGroup",
    "ec2:AuthorizeSecurityGroupIngress",
    "ec2:AuthorizeSecurityGroupEgress",
    "ec2:RevokeSecurityGroupEgress",
    # Standalone rule resources (aws_vpc_security_group_ingress_rule in
    # modules/service) use the SecurityGroupRule API, not Authorize*.
    "ec2:CreateSecurityGroupRule",
    "ec2:DeleteSecurityGroupRule",
    "ec2:CreateVpcEndpoint",
    "ec2:DeleteVpcEndpoints",
    "ec2:CreateTags",
    "ec2:Describe*",
  ]

  # Compute (modules/ecs, modules/service, modules/migration): cluster, task
  # definitions, services.
  ecs_actions = [
    "ecs:CreateCluster",
    "ecs:DeleteCluster",
    "ecs:PutClusterCapacityProviders",
    "ecs:RegisterTaskDefinition",
    "ecs:DeregisterTaskDefinition",
    "ecs:CreateService",
    "ecs:UpdateService",
    "ecs:DeleteService",
    "ecs:TagResource",
    "ecs:Describe*",
    "ecs:ListClusters",
    "ecs:ListServices",
    "ecs:ListTaskDefinitions",
  ]

  # Container registry (modules/ecr).
  ecr_actions = [
    "ecr:CreateRepository",
    "ecr:DeleteRepository",
    "ecr:PutLifecyclePolicy",
    "ecr:PutImageScanningConfiguration",
    "ecr:SetRepositoryPolicy",
    "ecr:TagResource",
    "ecr:DescribeRepositories",
    "ecr:ListTagsForResource",
  ]

  # Load balancer (modules/alb).
  alb_actions = [
    "elasticloadbalancing:CreateLoadBalancer",
    "elasticloadbalancing:DeleteLoadBalancer",
    "elasticloadbalancing:CreateTargetGroup",
    "elasticloadbalancing:DeleteTargetGroup",
    "elasticloadbalancing:CreateListener",
    "elasticloadbalancing:DeleteListener",
    "elasticloadbalancing:CreateRule",
    "elasticloadbalancing:ModifyListener",
    "elasticloadbalancing:AddTags",
    "elasticloadbalancing:Describe*",
  ]

  # IAM (modules/iam): OIDC provider, deploy/task roles and their policies.
  iam_actions = [
    "iam:CreateRole",
    "iam:DeleteRole",
    "iam:GetRole",
    "iam:PutRolePolicy",
    "iam:DeleteRolePolicy",
    "iam:AttachRolePolicy",
    "iam:DetachRolePolicy",
    "iam:PassRole",
    "iam:UpdateAssumeRolePolicy",
    "iam:CreateOpenIDConnectProvider",
    "iam:DeleteOpenIDConnectProvider",
    "iam:TagRole",
    "iam:GetRolePolicy",
    "iam:ListRolePolicies",
    "iam:ListAttachedRolePolicies",
    "iam:GetOpenIDConnectProvider",
  ]

  # Logs (Container Insights / task log groups).
  logs_actions = [
    "logs:CreateLogGroup",
    "logs:DeleteLogGroup",
    "logs:PutRetentionPolicy",
    "logs:TagResource",
    "logs:DescribeLogGroups",
  ]

  # SSM parameters publishing the deploy network config.
  ssm_actions = [
    "ssm:PutParameter",
    "ssm:DeleteParameter",
    "ssm:AddTagsToResource",
    "ssm:GetParameter",
    "ssm:GetParameters",
    "ssm:ListTagsForResource",
  ]

  # Secrets Manager (app key + broker URL containers; values set out of band).
  secrets_actions = [
    "secretsmanager:CreateSecret",
    "secretsmanager:DeleteSecret",
    "secretsmanager:TagResource",
    "secretsmanager:DescribeSecret",
    "secretsmanager:GetResourcePolicy",
    # Secret-version resources (rabbitmq URL, RDS credentials/DSN) write and
    # read back the value.
    "secretsmanager:PutSecretValue",
    "secretsmanager:GetSecretValue",
  ]

  # Object storage (modules/s3 media + modules/s3-backup): buckets and their
  # versioning/encryption/public-access/lifecycle/CORS configuration.
  s3_actions = [
    "s3:CreateBucket",
    "s3:DeleteBucket",
    "s3:PutBucketVersioning",
    "s3:PutEncryptionConfiguration",
    "s3:PutBucketPublicAccessBlock",
    "s3:PutBucketOwnershipControls",
    "s3:GetBucketOwnershipControls",
    "s3:DeleteBucketOwnershipControls",
    "s3:PutLifecycleConfiguration",
    "s3:PutBucketCORS",
    "s3:PutBucketTagging",
    "s3:GetBucketLocation",
    "s3:GetEncryptionConfiguration",
    "s3:GetBucketPublicAccessBlock",
  ]

  # Message broker (modules/rabbitmq): Amazon MQ for RabbitMQ.
  mq_actions = [
    "mq:CreateBroker",
    "mq:DeleteBroker",
    "mq:UpdateBroker",
    "mq:CreateTags",
    "mq:DescribeBroker",
    "mq:ListBrokers",
  ]

  # Managed database (modules/rds): only required when the env provisions RDS.
  rds_actions = [
    "rds:CreateDBInstance",
    "rds:DeleteDBInstance",
    "rds:ModifyDBInstance",
    "rds:CreateDBSubnetGroup",
    "rds:DeleteDBSubnetGroup",
    "rds:AddTagsToResource",
    "rds:DescribeDBInstances",
    "rds:DescribeDBSubnetGroups",
  ]

  # Scheduled Fargate tasks (modules/scheduled-task): EventBridge Scheduler.
  scheduled_task_actions = [
    "scheduler:CreateSchedule",
    "scheduler:DeleteSchedule",
    "scheduler:UpdateSchedule",
    "scheduler:GetSchedule",
    "scheduler:TagResource",
    # The module also creates a schedule group around the schedule.
    "scheduler:CreateScheduleGroup",
    "scheduler:DeleteScheduleGroup",
    "scheduler:GetScheduleGroup",
  ]

  # Aggregate the enabled areas. Sorted+deduped so the output is stable and the
  # guard reports a clean, ordered list.
  _actions = concat(
    local.state_actions,
    local.guard_actions,
    local.networking_actions,
    local.ecs_actions,
    local.ecr_actions,
    local.alb_actions,
    local.iam_actions,
    local.logs_actions,
    local.ssm_actions,
    local.secrets_actions,
    local.s3_actions,
    local.mq_actions,
    var.include_rds ? local.rds_actions : [],
    var.include_scheduled_tasks ? local.scheduled_task_actions : [],
  )
}
