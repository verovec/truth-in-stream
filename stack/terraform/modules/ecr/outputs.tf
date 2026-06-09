output "repository_urls" {
  value       = { for k, r in aws_ecr_repository.main : k => r.repository_url }
  description = "Map of image name to ECR repository URL."
}

output "repository_arns" {
  value       = [for r in aws_ecr_repository.main : r.arn]
  description = "ARNs of all repositories (for IAM scoping)."
}
