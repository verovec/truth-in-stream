package connector

import (
	"fmt"
	"strings"

	cron "github.com/robfig/cron/v3"
)

// SecretRef declares one producer secret a source needs on the crawler host.
// EnvVar is the environment variable the producer reads it from; SecretSuffix is
// the Secrets Manager id under <project>/<env>/ the host materializes it from.
// scripts/ingest-fetch-env.sh reads these from the manifest and fetches them on
// the crawler host, so declaring one actually wires the fetch (its ARN must also
// be added to the crawler host's secret_arns in stack/terraform/dev/main.tf). A
// secret is NEVER listed in ForwardEnv: it is read only from Secrets Manager on
// the host, so it never travels through the operator's SSM command payload and is
// never logged.
type SecretRef struct {
	EnvVar       string `json:"env_var"`
	SecretSuffix string `json:"secret_suffix"`
}

// Descriptor is the pure-data declaration of one ingestion source. It is the
// single source of truth both the Go scheduler and the shell host tooling read,
// so a source's cron, queue, compose services, forwarded env, and secrets are
// declared once. It carries no behavior: the cmd layer supplies the builder that
// constructs the producer, because only the cmd layer may import the broker and
// config.
type Descriptor struct {
	// Name is the stable source id (e.g. "wikipedia"), lower-case and used to
	// derive the SCHEDULE_<NAME>_* env keys and to select the source on the host.
	Name string `json:"name"`
	// DefaultCron is the always-on local scheduler cadence (a 5-field cron spec).
	// Empty means the source is host-only and on-demand: it is operable through
	// the ingestion hosts but never run by the local scheduler, so it has no
	// SCHEDULE_<NAME>_* knobs. See Schedulable.
	DefaultCron string `json:"default_cron,omitempty"`
	// Producer is the docker-compose.ingest.yml service the crawler host runs
	// (one-shot: fills the queue and exits).
	Producer string `json:"producer"`
	// Worker is the docker-compose.ingest.yml service the consumer host runs
	// (long-running: drains the queue into the database).
	Worker string `json:"worker"`
	// Queue is the versioned-queue base the producer publishes to and the worker
	// drains.
	Queue string `json:"queue"`
	// RequiredEnv is the non-secret producer env that must be set for a crawler
	// run; a missing one fails the host command fast. It must be a subset of
	// ForwardEnv.
	RequiredEnv []string `json:"required_env,omitempty"`
	// ForwardEnv is the non-secret producer config the operator forwards into the
	// host command. It never contains a secret (those are in Secrets).
	ForwardEnv []string `json:"forward_env,omitempty"`
	// Secrets are the API keys the host materializes from Secrets Manager. They
	// are never forwarded and never logged.
	Secrets []SecretRef `json:"secrets,omitempty"`
	// NewQueue is true only when the source introduces a queue no existing worker
	// drains, so the consumer host needs a new worker service. A source that
	// reuses an existing job shape leaves it false and reuses the existing worker.
	NewQueue bool `json:"new_queue,omitempty"`
}

// Schedulable reports whether the source runs on the always-on local scheduler.
// A host-only source (no DefaultCron) is operable through the ingestion hosts but
// is never registered on the scheduler's cron loop.
func (d Descriptor) Schedulable() bool { return d.DefaultCron != "" }

// EnvPrefix is the upper-cased, separator-normalised form of the name used to
// build the SCHEDULE_<PREFIX>_ENABLED / SCHEDULE_<PREFIX>_CRON env keys, so the
// scheduler config for a source is derived from its name, not hand-declared.
func (d Descriptor) EnvPrefix() string {
	var b strings.Builder
	for _, r := range strings.ToUpper(d.Name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Validate rejects a descriptor that would break the fleet wiring: a missing
// name/producer/worker/queue, a malformed DefaultCron, a RequiredEnv that is not
// forwarded (so it could never reach the producer), or a secret that is also
// forwarded (which would leak it through the SSM command). It is the invariant
// every registry entry must satisfy.
func (d Descriptor) Validate() error {
	switch {
	case d.Name == "":
		return fmt.Errorf("connector: descriptor has empty name")
	case d.Producer == "":
		return fmt.Errorf("connector %q: empty producer service", d.Name)
	case d.Worker == "":
		return fmt.Errorf("connector %q: empty worker service", d.Name)
	case d.Queue == "":
		return fmt.Errorf("connector %q: empty queue", d.Name)
	}
	if d.DefaultCron != "" {
		if _, err := cron.ParseStandard(d.DefaultCron); err != nil {
			return fmt.Errorf("connector %q: invalid default cron %q: %w", d.Name, d.DefaultCron, err)
		}
	}
	forwarded := make(map[string]struct{}, len(d.ForwardEnv))
	for _, e := range d.ForwardEnv {
		if e == "" {
			return fmt.Errorf("connector %q: empty forward env entry", d.Name)
		}
		forwarded[e] = struct{}{}
	}
	for _, e := range d.RequiredEnv {
		if _, ok := forwarded[e]; !ok {
			return fmt.Errorf("connector %q: required env %q is not in ForwardEnv, so it can never reach the producer", d.Name, e)
		}
	}
	for _, s := range d.Secrets {
		if s.EnvVar == "" || s.SecretSuffix == "" {
			return fmt.Errorf("connector %q: secret needs both an env var and a secret suffix, got %+v", d.Name, s)
		}
		if _, ok := forwarded[s.EnvVar]; ok {
			return fmt.Errorf("connector %q: secret %q must not be forwarded (it is read from Secrets Manager on the host)", d.Name, s.EnvVar)
		}
	}
	return nil
}
