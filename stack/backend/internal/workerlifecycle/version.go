// Package workerlifecycle holds the decision logic for the embedding-worker
// lifecycle lambda: queue-depth autoscaling and version-drain rollout cleanup.
// It is pure and transport-agnostic - it knows nothing about AWS, the ECS API,
// or the RabbitMQ wire format - so every rule here is unit-tested without a live
// broker or cluster, and the lambda entrypoint wires it to ECS, SSM and the
// management API.
package workerlifecycle

import "strings"

// versionMarker separates a queue base name from its version token, mirroring
// the producer/worker convention <base>.v<version> (config.Queue.VersionedName).
const versionMarker = ".v"

// VersionedQueueName returns the broker queue name for one version of a base,
// e.g. ("embedding.jobs", "3") -> "embedding.jobs.v3".
func VersionedQueueName(base, version string) string {
	return base + versionMarker + version
}

// ActiveVersion returns the active (newest) version from a comma-separated
// RABBITMQ_QUEUE_VERSIONS value (oldest-first, the worker's convention), and
// whether a non-empty version was present. Surrounding spaces are trimmed and
// empty tokens ignored, so a trailing comma or padding never yields a blank
// active version.
func ActiveVersion(rawVersions string) (string, bool) {
	active := ""
	for _, tok := range strings.Split(rawVersions, ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			active = tok
		}
	}
	if active == "" {
		return "", false
	}
	return active, true
}

// NewestVersion returns the highest version among versions using a numeric-aware
// ordering: when two versions are both runs of decimal digits they are compared
// by value (so "10" outranks "2"), otherwise lexicographically (which orders
// fixed-width date stamps like "20260407" correctly). versions must be non-empty.
func NewestVersion(versions []string) string {
	newest := versions[0]
	for _, v := range versions[1:] {
		if versionLess(newest, v) {
			newest = v
		}
	}
	return newest
}

// versionLess reports whether a orders before b: numerically when both are
// all-digit (shorter is smaller, then lexicographic to disambiguate equal
// lengths), lexicographically otherwise.
func versionLess(a, b string) bool {
	if isDigits(a) && isDigits(b) {
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		return a < b
	}
	return a < b
}

// queueVersion extracts the version token from a versioned queue name for base,
// e.g. ("embedding.jobs.v3", "embedding.jobs") -> ("3", true). It reports false
// for any name that is not <base>.v<token> with a valid token.
func queueVersion(name, base string) (string, bool) {
	token, ok := strings.CutPrefix(name, base+versionMarker)
	if !ok || !isVersionToken(token) {
		return "", false
	}
	return token, true
}

// versionedDepths reduces depths (queue name -> backlog) to the per-version
// backlog of base's versioned queues, dropping every name that is not
// <base>.v<token>.
func versionedDepths(depths map[string]int64, base string) map[string]int64 {
	out := make(map[string]int64, len(depths))
	for name, depth := range depths {
		if v, ok := queueVersion(name, base); ok {
			out[v] = depth
		}
	}
	return out
}

// isVersionToken mirrors the producer/worker version grammar: a non-empty run of
// letters, digits, '_' or '-'.
func isVersionToken(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func isDigits(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
