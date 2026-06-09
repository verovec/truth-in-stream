output "dns_name" {
  value       = aws_lb.main.dns_name
  description = "Public DNS name of the load balancer."
}

output "listener_arn" {
  value       = local.https ? aws_lb_listener.https[0].arn : aws_lb_listener.http.arn
  description = "Listener that services attach their routing rules to."
}

output "arn" {
  value       = aws_lb.main.arn
  description = "Load balancer ARN."
}
