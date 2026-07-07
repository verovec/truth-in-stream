variable "project" {
  type        = string
  description = "Project slug used to namespace resources."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "name" {
  type        = string
  description = "Host role suffix, appended to the project/environment prefix for the Name tag and resource names (e.g. crawler-host, consumer-host). Scripts resolve the instance by this Name tag, as the tunnels resolve the bastion today."
}

variable "vpc_id" {
  type        = string
  description = "VPC the host's security group is created in."
}

variable "subnet_id" {
  type        = string
  description = "Private subnet the host launches in. SSM reaches it over the subnet's NAT egress; it has no public IP."
}

variable "instance_type" {
  type        = string
  default     = "t3.small"
  description = "Ingestion-host instance class. Must be an x86_64 family: the AMI is resolved from the al2023 x86_64 SSM parameter, so a Graviton type (e.g. t4g.small) would not boot. Sized larger than the bastion because the host runs the producer/worker containers, not just an SSM relay; tune per host role (a lighter crawler, a heavier consumer)."

  validation {
    condition     = !can(regex("^[a-z][0-9]+g[a-z]*\\.", var.instance_type))
    error_message = "instance_type must be an x86_64 family; the AMI is x86_64 (a Graviton type like t4g.small would not boot)."
  }
}

variable "docker_compose_version" {
  type        = string
  default     = "v5.3.1"
  description = "Docker Compose plugin release tag the host installs at boot (AL2023 has no docker-compose-plugin package, so the binary is fetched from the pinned github.com/docker/compose release). Pinned, never :latest, so a host boots a reproducible Compose version; bump it here to move all hosts. Default is the current latest stable (verified against the docker/compose releases at authoring); the download is exercised at the human-gated apply, so the operator reconfirms the tag then."

  validation {
    condition     = can(regex("^v[0-9]+\\.[0-9]+\\.[0-9]+$", var.docker_compose_version))
    error_message = "docker_compose_version must be an explicit release tag like v5.3.1 (never a moving ref like latest)."
  }
}

variable "secret_arns" {
  type        = list(string)
  description = "Exact Secrets Manager ARNs this host's instance profile may read with secretsmanager:GetSecretValue - never a wildcard. Scope to only the secrets the host's pipeline services consume (e.g. the broker URL, the RDS DSN, the API keys the producers/workers use); the host materializes its env file from these at run time."

  validation {
    condition     = length(var.secret_arns) > 0
    error_message = "secret_arns must list at least one secret ARN; the host needs the broker URL at minimum. An empty list would grant GetSecretValue on nothing, which is never intended."
  }
}

variable "ecr_repository_arns" {
  type        = list(string)
  description = "ECR repository ARNs the host may pull images from (the backend repository holding the compiled ingestion binaries). Scopes the layer/image-pull actions; the account-level ecr:GetAuthorizationToken is granted separately on all resources, as ECR requires."

  validation {
    condition     = length(var.ecr_repository_arns) > 0
    error_message = "ecr_repository_arns must list at least the backend repository ARN so the host can pull the ingestion image."
  }
}
