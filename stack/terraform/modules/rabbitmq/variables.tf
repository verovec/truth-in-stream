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
  description = "VPC the broker's security group is created in."
}

variable "subnet_ids" {
  type        = list(string)
  description = "Private subnets the broker is launched in. A SINGLE_INSTANCE deployment takes exactly one subnet; a CLUSTER_MULTI_AZ deployment takes the private subnets across AZs."
}

variable "allowed_security_group_ids" {
  type        = list(string)
  description = "Security groups permitted to reach the broker on AMQPS and the management console (the ECS tasks security group)."
}

variable "management_allowed_security_group_ids" {
  type        = list(string)
  default     = []
  description = "Security groups permitted to reach the RabbitMQ management API over HTTPS (port 443). Empty by default, so the management console stays closed to the application security groups; the metrics-poller lambda is the only intended grantee. Backward compatible: an empty list adds no ingress."
}

variable "engine_version" {
  type        = string
  default     = "3.13"
  description = "Amazon MQ RabbitMQ engine version. 3.13 runs on mq.t3/mq.m5/mq.m7g; the 4.x series is mq.m7g-only, so it is a deliberate, costlier upgrade. Verify the current support matrix with `aws mq describe-broker-instance-options` before changing."
}

variable "host_instance_type" {
  type        = string
  default     = "mq.t3.micro"
  description = "Broker instance class. mq.t3.micro is the smallest RabbitMQ instance and suits the queue's current load (foundation only); scale up when the worker fleet drives real throughput."
}

variable "deployment_mode" {
  type        = string
  default     = "SINGLE_INSTANCE"
  description = "SINGLE_INSTANCE (one node, one subnet) or CLUSTER_MULTI_AZ (three nodes for HA, mq.m5/mq.m7g only). The queue carries no consumer yet, so the foundation defaults to a single instance; HA is a later hardening step."

  validation {
    condition     = contains(["SINGLE_INSTANCE", "CLUSTER_MULTI_AZ"], var.deployment_mode)
    error_message = "deployment_mode must be SINGLE_INSTANCE or CLUSTER_MULTI_AZ."
  }
}

variable "username" {
  type        = string
  default     = "app"
  description = "Broker admin username. The password is generated and never set in Terraform variables or state input."
}

variable "secret_recovery_window_days" {
  type        = number
  default     = 7
  description = "Recovery window for the connection-URL secret. Dev sets 0 so destroy/apply cycles do not collide with the recovery window."
}
