output "instance_id" {
  value       = aws_instance.bastion.id
  description = "Bastion EC2 instance ID (the SSM port-forward target)."
}

output "security_group_id" {
  value       = aws_security_group.bastion.id
  description = "Bastion security group. Add it to the allow-list of whatever the tunnel must reach: the broker's allowed_security_group_ids for the AMQPS local-worker tunnel (dev), or the postgres SG's database_client_security_group_ids for the RDS load tunnel (prod)."
}

output "name" {
  value       = local.name
  description = "Name tag the port-forward script resolves the instance by."
}
