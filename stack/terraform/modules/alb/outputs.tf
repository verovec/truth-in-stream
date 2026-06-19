output "dns_name" {
  value       = aws_lb.main.dns_name
  description = "DNS name of the load balancer. Internal (not publicly resolvable) when internal = true; otherwise the public ALB DNS name."
}

output "security_group_id" {
  value       = var.internal ? aws_security_group.internal[0].id : var.security_group_id
  description = "Security group fronting the load balancer: the module-owned restricted SG when internal = true, otherwise the passed-in SG. Services grant their task SG ingress from this."
}

output "listener_arn" {
  value       = local.https ? aws_lb_listener.https[0].arn : aws_lb_listener.http.arn
  description = "Listener that services attach their routing rules to."
}

output "arn" {
  value       = aws_lb.main.arn
  description = "Load balancer ARN."
}

output "arn_suffix" {
  value       = aws_lb.main.arn_suffix
  description = "ARN suffix of the load balancer, the LoadBalancer dimension value on AWS/ApplicationELB CloudWatch metrics."
}
