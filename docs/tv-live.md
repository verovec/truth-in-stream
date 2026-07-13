# Live TV capture (on-demand cloud recorder)

Operator runbook for the **TV capture** worker: a long-running headless viewer that watches
enabled TV channels, feeds their audio into the live fact-check pipeline, and (optionally)
archives the broadcast to S3. It runs on its own on-demand EC2 host, the same SSM-only,
stop/start-able model as the [ingestion hosts](ingestion-hosts.md), so idle cost between runs is
only the host's EBS volume.

- Worker: `stack/backend/cmd/tvcapture` (long-running), `stack/backend/internal/tvcapture`
- Channel registry + seed: `stack/backend/internal/seed/tv_channel.go`, migration `0015_tv_channels`
- Cloud compose service: [`docker-compose.ingest.yml`](../docker-compose.ingest.yml) service `tvcapture`
- Host (Terraform): `stack/terraform/modules/ingestion-host`, instantiated in
  `stack/terraform/dev` as `module "tvcapture_host"` behind `enable_ingestion_hosts` (default off).
- Config: `stack/backend/internal/config` `LoadTVCapture` — see the
  [TV capture env vars](configuration.md#tv-capture).

## What it is

The worker authenticates to the backend as the scoped Keycloak `tv-capture` service client
(client-credentials grant, **not** an admin credential) and then, on a poll loop:

1. Lists the enabled channels from the backend channel registry.
2. For each enabled channel, resolves the live stream and runs a `streamlink | ffmpeg` capture
   pipeline that transcodes the audio to PCM.
3. Streams that PCM into the backend's live feed WebSocket, so the channel is fact-checked live
   through the same pipeline as an imported video.
4. When the channel has archiving armed, it also writes hourly MPEG-TS segments and uploads each
   one to S3 as a `tv` recording via the backend's presign/register API.

The worker reconciles continuously: enabling a channel in the backoffice starts a pipeline within
one poll interval; disabling it tears the pipeline down.

### Why streamlink, not YouTube PO-tokens

YouTube's live delivery increasingly requires a "proof-of-origin" (PO) token minted by a real
browser session. We deliberately **do not** implement PO-token minting: it means driving a headless
browser and tracking an evolving anti-bot handshake, which is brittle and adversarial. Instead the
worker uses `streamlink` against the public official channel URL and accepts that a given YouTube
live may occasionally be unresolvable; a channel that cannot be resolved is skipped and retried on
the next poll, never crashing the worker.

## Enabling a channel end to end

1. **Provision + start the host** (human-gated, see Prerequisites) and bring the `tvcapture` service
   up on it (see Usage).
2. **Toggle the channel on** in the admin [backoffice](backoffice.md) TV section. Every seed channel
   ships **disabled**, with archiving **armed** (so an enabled channel records unless you opt out
   per channel).
3. The worker picks the change up on its next poll (`TV_CAPTURE_POLL_SECONDS`, default 30s),
   resolves the stream, and starts capture + live analysis. If archiving is armed for that channel,
   it also begins writing and uploading segments.
4. Watch the results on the channel's live page; watch the host's logs in CloudWatch under
   `/truth-in-stream/<env>/ingest/tvcapture`.

## Legal posture per source class

TV capture is scoped to **free, non-DRM** sources only. The seed registry
(`internal/seed/tv_channel.go`) enumerates two classes:

- **DRM commercial broadcasters (TF1, France 2, M6, …): out of scope.** Their live streams are
  encrypted/geo-DRM'd; circumventing that protection is not something this project does. They are
  not in the registry.
- **Official 24/7 YouTube lives** (franceinfo, France 24 FR, BFMTV, Euronews FR, LCP, Public Sénat,
  CNEWS, LCI). Embedding these official streams is sanctioned, but **stream-ripping and archiving
  them conflicts with YouTube's Terms of Service.** So archiving is **per-channel opt-in** and paired
  with **short retention** — the recording exists only as a short-lived working copy for analysis,
  not a durable rebroadcast archive.
- **Parliamentary HLS portals** (Assemblée nationale, Sénat). These are public-broadcast under a
  constitutional mandate and align with the Etalab open-data precedent, so they are the **cleanest
  sources to archive**. They are plain HLS (`videos.assemblee-nationale.fr/direct`,
  `videos.senat.fr/direct`), no DRM.

The `source_ref` stored per channel is the stable channel/portal URL, never a resolved manifest; the
worker re-resolves it on every capture start.

## Retention

Recording retention is **app-enforced and authoritative**: the worker runs a daily prune that
deletes each expired recording's S3 object **and** its database row, bounded by
`TV_RECORDING_RETENTION_DAYS` (default 30, per the design's short-retention posture for YouTube
sources). Set it low for the ToS-constrained YouTube class.

An **optional S3 lifecycle backstop** exists as a safety net only: the `media` bucket module takes a
`recordings_retention_days` variable (default `0` = disabled) that, when set `> 0`, adds a
prefix-scoped lifecycle rule expiring objects under `recordings/`. Leave it `0` in normal operation —
the app-level prune is the source of truth; enable it only to guard against a stuck worker leaving
recordings behind. It is wired but off in both `dev` and `prod`.

## Salvage and crash behaviour

The `tvcapture` compose service mounts a persistent `tv-capture-work` volume at the worker's work
dir (`TV_CAPTURE_WORK_DIR`, default `/work`). In-progress `.ts` segments are written there before
upload, so a host restart or worker crash leaves the partial segments on disk; on startup the worker
runs a **salvage pass** that finishes and uploads recoverable segments rather than discarding them.
The service runs `restart: unless-stopped` with a 120s stop grace period, and the worker **idles
without exiting** when `TV_CAPTURE_ENABLED` is unset (so it never enters a restart loop when capture
is turned off).

## Secrets consumed (Secrets Manager)

The tvcapture host's instance profile is scoped to exactly these secret ARNs; the host materializes
its env from them via [`scripts/ingest-fetch-env.sh`](../scripts/ingest-fetch-env.sh) `tvcapture`.
The worker uses **neither the broker nor RDS**, so — unlike the crawler/consumer hosts — it holds no
broker URL and no database DSN:

- `truth-in-stream/<env>/app/tv-capture-client-secret` -> `TV_CAPTURE_CLIENT_SECRET`
  (the Keycloak `tv-capture` client-credentials secret).
- `truth-in-stream/<env>/app/slack-webhook-url` -> `SLACK_WEBHOOK_URL` (run/crash alerts; optional).

S3 archiving needs **no S3 IAM on the host**: uploads go through backend-issued **presigned PUT**
URLs, which carry their own auth.

## Usage

The `tvcapture` service is a long-running detached service, distinct from the queue-draining
`/consumer` workers, so it is brought up by hand over SSM rather than through the `/consumer`
command. Once the host is provisioned and started (Prerequisites), from the host (an SSM session, or
adapt the pattern in [`scripts/ingest-host.sh`](../scripts/ingest-host.sh)):

```bash
# On the tvcapture host: materialize the env from Secrets Manager, then bring the
# long-running service up detached.
bash scripts/ingest-fetch-env.sh tvcapture dev
INGEST_IMAGE="<account>.dkr.ecr.eu-west-3.amazonaws.com/truth-in-stream-dev-backend:latest" \
INGEST_ENV=dev \
TV_CAPTURE_ENABLED=true \
docker compose -f docker-compose.ingest.yml up -d tvcapture

# Follow logs / stop:
docker compose -f docker-compose.ingest.yml logs -f tvcapture
docker compose -f docker-compose.ingest.yml down            # or: stop the host to cap cost
```

`TV_CAPTURE_BACKEND_URL` and `TV_CAPTURE_TOKEN_URL` are operator-provided (see Prerequisites); the
rest of the env comes from Secrets Manager (`ingest-fetch-env.sh`) or the config defaults. Stop the
host (`aws ec2 stop-instances`) between capture windows so idle cost drops to the EBS volume.

## Cost-control lifecycle

Provision on demand, run capture for the window you need, stop the host. The worker's salvage pass
and the app-level prune mean stopping the host is safe: partial segments are recovered on the next
start, and expired recordings are cleaned up by retention. Between runs the host bills only its EBS
volume.

## Prerequisites (human-gated, deferred to the operator)

1. **Fill the account id.** `deploy/targets.json`'s `<env>.account_id` placeholder must hold the real
   account id, or the account guard refuses every run (same gate as the ingestion hosts).
2. **Provision the host.** It lives behind `enable_ingestion_hosts` (default off). Apply is
   human-gated: `terraform apply -var enable_ingestion_hosts=true` in `stack/terraform/dev`.
3. **Push the secrets out of band.** Terraform creates the `tv-capture-client-secret` and
   `slack-webhook-url` containers empty; set their values with
   `make push-secrets ENV=dev` (allowlisted in [`scripts/push-secrets.sh`](../scripts/push-secrets.sh))
   or `aws secretsmanager put-secret-value`. A secret the host cannot read fails the run loudly.
4. **Create the Keycloak `tv-capture` client.** A confidential service-account client with the
   client-credentials grant enabled, carrying the scoped **`tv-capture` realm role** (not `admin`).
   The backend must accept its `azp`: set `KEYCLOAK_ADDITIONAL_CLIENT_IDS` to include `tv-capture`
   wherever the backend runs (see [Configuration -> TV capture](configuration.md#tv-capture)). The
   client secret is what you push in step 3.
5. **Backend + Keycloak reachable from the host.** The worker talks to the backend HTTP API + feed
   WebSocket (`TV_CAPTURE_BACKEND_URL`) and to the Keycloak token endpoint (`TV_CAPTURE_TOKEN_URL`,
   derived from `KEYCLOAK_ISSUER` if unset). Reachability is a **networking prerequisite**, not wired
   in Terraform: point these at the internal ALB address or the public CloudFront URL the host can
   reach. The tvcapture host's security group is deliberately **not** admitted to the broker or RDS
   (it uses neither).
