output "account_id" {
  value       = data.aws_caller_identity.current.account_id
  description = "AWS account ID Terraform is operating against."
}
