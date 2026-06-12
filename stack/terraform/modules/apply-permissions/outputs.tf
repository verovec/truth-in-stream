output "actions" {
  value       = sort(distinct(local._actions))
  description = "Sorted, deduped IAM actions the apply role must hold to provision this environment. Surfaced by each env root as the apply_required_actions output the pre-apply guard checks."
}
