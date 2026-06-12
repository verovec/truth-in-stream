# Roadmap - Truth in Stream (ready-queue snapshot)

## State

Local ingestion-rework chain complete and merged: VER-49 -> 52 -> 53 -> 54 -> 55 (all Done).
Cloud-infra chain in flight in another session.

## Open cards

- VER-59 (High) Least-privilege CI/CD IAM roles - Done (merged)
- VER-60 (High) Amazon MQ broker + queue versioning - In Progress (other session); blocked VER-61
- VER-61 (High) Deployable producer ECS job - blockedBy VER-60 (In Progress), VER-53 (Done) -> NOT ready (dep not yet In Review/Done)
- VER-62 (High) SSM bastion + tunnel - blockedBy VER-61 -> NOT ready
- VER-63 (Med) RabbitMQ metrics lambda + dashboard - blockedBy VER-62 -> NOT ready
- VER-64 (Med) Worker-lifecycle lambda autoscaling - chain -> NOT ready
- VER-65 (Med) Deploy workflow for producer/worker images - chain -> NOT ready

## Ready queue

(empty) - the only unblockable card, VER-61, waits on VER-60 reaching In Review; VER-60 is
being delivered by another session right now. Re-check once its PR opens.
