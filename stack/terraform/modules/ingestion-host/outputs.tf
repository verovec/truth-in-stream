output "instance_id" {
  value       = aws_instance.host.id
  description = "Ingestion-host EC2 instance ID. The operator's scripts start/stop it and open SSM sessions to it."
}

output "security_group_id" {
  value       = aws_security_group.host.id
  description = "Ingestion-host security group (egress-only, no inbound). Add it to the broker's allowed_security_group_ids for AMQPS 5671 and the RDS/postgres SG's ingress for 5432 so the host can publish to the queue and write to the database."
}

output "name" {
  value       = local.name
  description = "Name tag the operator scripts resolve the instance by."
}
