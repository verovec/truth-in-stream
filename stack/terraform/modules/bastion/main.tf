locals {
  name = "${var.project}-${var.environment}-bastion"
}

# Latest Amazon Linux 2023 AMI, resolved from the SSM public parameter. AL2023
# ships the SSM agent preinstalled, so the instance registers with Session
# Manager on boot with no user data. ignore_changes on the AMI (below) stops a
# new AL2023 release from forcing a replacement on every plan.
data "aws_ssm_parameter" "al2023_ami" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64"
}

# Egress-only: the bastion has no inbound rules at all. SSM access is an
# outbound TLS session the agent opens to the Session Manager endpoints (reached
# over the NAT), so no ingress is ever required. Outbound is left open so the
# port-forward can reach the private target (the broker on AMQPS in dev, RDS on
# 5432 in prod) and the agent can reach SSM, Secrets Manager, and EC2 Messages;
# scoping egress to the target SG would create a dependency cycle with a target
# that allows this SG inbound (e.g. the broker).
resource "aws_security_group" "bastion" {
  name        = local.name
  description = "SSM-only bastion: no inbound, outbound only (SSM port-forward to a private target: broker or RDS)"
  vpc_id      = var.vpc_id

  egress {
    description = "All outbound (SSM endpoints over TLS, and the private target: broker AMQPS or RDS 5432)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = local.name }
}

# Instance role limited to the SSM managed-core policy: it grants exactly the
# ssm/ssmmessages/ec2messages actions Session Manager needs and nothing else
# (no SSH key material, no broader access).
resource "aws_iam_role" "bastion" {
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

resource "aws_iam_role_policy_attachment" "bastion_ssm" {
  role       = aws_iam_role.bastion.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "bastion" {
  name = local.name
  role = aws_iam_role.bastion.name
  tags = { Name = local.name }
}

resource "aws_instance" "bastion" {
  ami                    = data.aws_ssm_parameter.al2023_ami.value
  instance_type          = var.instance_type
  subnet_id              = var.subnet_id
  iam_instance_profile   = aws_iam_instance_profile.bastion.name
  vpc_security_group_ids = [aws_security_group.bastion.id]

  # Private subnet, no public IP: the bastion is reachable only through SSM.
  associate_public_ip_address = false

  # IMDSv2 required: token-authenticated metadata only, blocking SSRF-style
  # credential theft against the instance role.
  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
  }

  lifecycle {
    # A new AL2023 AMI release would otherwise replace the bastion on every
    # plan; recycle it deliberately, not on Amazon's cadence.
    ignore_changes = [ami]
  }

  tags = { Name = local.name }
}
