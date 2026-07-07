locals {
  name = "${var.project}-${var.environment}-${var.name}"
}

# Latest Amazon Linux 2023 AMI, resolved from the SSM public parameter (AL2023
# ships the SSM agent preinstalled, so the host registers with Session Manager on
# boot). ignore_changes on the AMI (below) stops a new AL2023 release from forcing
# a replacement on every plan; the host is recycled deliberately, not on Amazon's
# cadence. Mirrors the bastion pattern.
data "aws_ssm_parameter" "al2023_ami" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64"
}

# Region/account/partition for the least-privilege CloudWatch Logs ARN scope.
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

# Egress-only: the host has no inbound rules at all. SSM access is an outbound TLS
# session the agent opens to the Session Manager endpoints (reached over the NAT),
# so no ingress is ever required - no SSH, no open ports. Outbound is left open so
# the host can reach SSM/Secrets Manager/ECR/CloudWatch (control plane), the
# private broker on AMQPS 5671 and RDS on 5432, GitHub for the Compose plugin at
# boot, and the external ingestion APIs. This SG is admitted by the broker (5671)
# and the RDS/postgres SG (5432) in the env root; the host initiates those
# connections, so it needs no inbound of its own.
resource "aws_security_group" "host" {
  name        = local.name
  description = "SSM-only ingestion host: no inbound, outbound only (broker AMQPS 5671, RDS 5432, AWS control plane, external ingestion APIs)"
  vpc_id      = var.vpc_id

  egress {
    description = "All outbound (SSM endpoints, broker AMQPS 5671, RDS 5432, ECR/Secrets/Logs, external ingestion APIs)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = local.name }
}

# Instance role: the SSM managed-core policy (Session Manager, no SSH key) plus a
# tightly scoped inline policy for exactly what the ingestion containers need -
# reading their secrets, pulling the backend image, and shipping logs.
resource "aws_iam_role" "host" {
  name = local.name

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })

  tags = { Name = local.name }
}

resource "aws_iam_role_policy_attachment" "host_ssm" {
  role       = aws_iam_role.host.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# Least-privilege runtime policy for the host. Every statement is scoped to exact
# resources: GetSecretValue only on this host's ingestion secret ARNs, image pull
# only on the backend repository, log writes only under this env's ingest log
# prefix. Only ecr:GetAuthorizationToken is unavoidably account-wide - AWS grants
# the ECR auth token at the registry level and rejects a resource-scoped ARN.
data "aws_iam_policy_document" "host" {
  statement {
    sid       = "ReadIngestionSecrets"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = var.secret_arns
  }

  statement {
    sid       = "EcrAuth"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid = "EcrPull"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
    ]
    resources = var.ecr_repository_arns
  }

  # CloudWatch Logs for the container log driver, scoped to this env's ingest log
  # groups. The trailing ":*" covers the group and its streams. Card C's compose
  # sends container logs to a group under this prefix.
  statement {
    sid = "WriteIngestLogs"
    actions = [
      "logs:CreateLogGroup",
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = ["arn:${data.aws_partition.current.partition}:logs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:log-group:/${var.project}/${var.environment}/ingest/*:*"]
  }
}

resource "aws_iam_role_policy" "host" {
  name   = "ingestion-host"
  role   = aws_iam_role.host.id
  policy = data.aws_iam_policy_document.host.json
}

resource "aws_iam_instance_profile" "host" {
  name = local.name
  role = aws_iam_role.host.name
  tags = { Name = local.name }
}

resource "aws_instance" "host" {
  ami                    = data.aws_ssm_parameter.al2023_ami.value
  instance_type          = var.instance_type
  subnet_id              = var.subnet_id
  iam_instance_profile   = aws_iam_instance_profile.host.name
  vpc_security_group_ids = [aws_security_group.host.id]

  # Private subnet, no public IP: the host is reachable only through SSM.
  associate_public_ip_address = false

  # Docker + Compose + git via cloud-init. Plaintext user_data (the provider
  # base64-encodes it); no secret is ever placed here - user-data lands in
  # instance metadata and state. replace_on_change so a script edit recreates the
  # host and cloud-init actually re-runs, instead of the change being silently
  # stored but never executed.
  user_data = templatefile("${path.module}/user-data.sh.tftpl", {
    docker_compose_version = var.docker_compose_version
  })
  user_data_replace_on_change = true

  # IMDSv2 required: token-authenticated metadata only, blocking SSRF-style
  # credential theft. hop_limit 2 (not the default 1) so containers reaching IMDS
  # through the Docker bridge can still fetch the instance-role credentials the
  # ECR pull and Secrets Manager reads depend on.
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 2
  }

  lifecycle {
    # A new AL2023 AMI release would otherwise replace the host on every plan;
    # recycle it deliberately, not on Amazon's cadence.
    ignore_changes = [ami]
  }

  tags = { Name = local.name }
}
