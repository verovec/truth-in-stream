output "repository_urls" {
  value       = { for k, r in aws_ecr_repository.main : k => r.repository_url }
  description = "Map of image name to ECR repository URL."
}

output "repository_arns" {
  value       = [for r in aws_ecr_repository.main : r.arn]
  description = "ARNs of all repositories (for IAM scoping)."
}

output "repository_arns_by_name" {
  value       = { for k, r in aws_ecr_repository.main : k => r.arn }
  description = "Map of image name to ECR repository ARN, for scoping an IAM policy to a single repository (e.g. the ingestion host pulling only the backend image)."
}
