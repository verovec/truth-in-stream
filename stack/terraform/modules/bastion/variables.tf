variable "project" {
  type        = string
  description = "Project slug used to namespace resources."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "vpc_id" {
  type        = string
  description = "VPC the bastion's security group is created in."
}

variable "subnet_id" {
  type        = string
  description = "Private subnet the bastion launches in. SSM reaches it over the subnet's NAT egress; it has no public IP."
}

variable "instance_type" {
  type        = string
  default     = "t3.micro"
  description = "Bastion instance class. Must be an x86_64 family: the AMI is resolved from the al2023 x86_64 SSM parameter, so a Graviton type (e.g. t4g.micro) would not boot. t3.micro is ample: the host only relays SSM port-forward traffic, it runs no workload."

  validation {
    condition     = !can(regex("^[a-z][0-9]+g[a-z]*\\.", var.instance_type))
    error_message = "instance_type must be an x86_64 family; the AMI is x86_64 (a Graviton type like t4g.micro would not boot)."
  }
}
