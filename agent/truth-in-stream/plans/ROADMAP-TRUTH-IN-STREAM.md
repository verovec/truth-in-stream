# Roadmap - Truth in Stream (ready-queue snapshot)

## Open cards (Todo unless noted)

- VER-55 (High) Full local corpus reingest - blockedBy VER-49,52,53,54 (all Done) -> READY off main
- VER-59 (High) Least-privilege CI/CD IAM roles - In Progress (other session); blockedBy VER-57 (Done); blocks VER-60
- VER-60 (High) Amazon MQ broker + queue versioning - blockedBy VER-59 (In Progress) -> NOT ready
- VER-61 (High) Deployable producer ECS job - blockedBy VER-60, VER-53 -> NOT ready
- VER-62 (High) SSM bastion + tunnel - blockedBy VER-61 -> NOT ready
- VER-63 (Med) RabbitMQ metrics lambda + dashboard - blockedBy VER-62 -> NOT ready
- VER-64 (Med) Worker-lifecycle lambda autoscaling - chain -> NOT ready
- VER-65 (Med) Deploy workflow for producer/worker images - chain -> NOT ready

## Ready queue

1. VER-55 - Full local corpus reingest after the ingestion rework (High) - off main; all deps Done

(VER-60..65 blocked behind the in-progress VER-59 IAM chain.)
