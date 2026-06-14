package workerlifecycle

import "time"

// ServiceScaling is the queue-depth autoscaling policy for one worker service.
type ServiceScaling struct {
	// QueueBase is the versioned-queue base whose newest version's backlog drives
	// scaling, e.g. "embedding.jobs".
	QueueBase string
	// Ratio is the target messages-per-worker: the raw desired count is
	// ceil(backlog / Ratio). Must be > 0 to scale.
	Ratio int
	// Min and Max clamp the result. Max == 0 disables the service - desired is
	// forced to 0 regardless of backlog - which is the gate that keeps the fleet
	// off until the workers move onto the ECS cluster.
	Min int
	Max int
	// Cooldown is the minimum time between scale actions for the service, so a
	// flapping backlog cannot churn the fleet every tick.
	Cooldown time.Duration
}

// ComputeDesiredCount returns the next desired replica count for the service
// given its current desired count and the backlog of its newest versioned queue.
//
// The raw target is ceil(backlog / Ratio). A single invocation moves at most one
// exponential step toward it - at most doubling when scaling up and at most
// halving when scaling down - so a transient backlog spike cannot slam the fleet
// to Max in one tick, and a drain cannot drop every worker at once mid-flight.
// The stepped value is clamped to [Min, Max]. Max == 0 disables the service and
// returns 0; a non-positive Ratio is a misconfiguration and returns current
// unchanged so a bad config never scales the fleet.
func (s ServiceScaling) ComputeDesiredCount(current int, backlog int64) int {
	if s.Max <= 0 {
		return 0
	}
	if s.Ratio <= 0 {
		return current
	}
	raw := 0
	if backlog > 0 {
		raw = int((backlog + int64(s.Ratio) - 1) / int64(s.Ratio))
	}
	return min(s.Max, max(s.Min, stepToward(current, raw)))
}

// stepToward returns the count one exponential step from current toward raw: at
// most double (from a floor of 1, so 0 -> 2 not 0 -> 0) when growing, at most
// halve when shrinking, exact when already there.
func stepToward(current, raw int) int {
	switch {
	case raw > current:
		return min(raw, max(1, current)*2)
	case raw < current:
		return max(raw, current/2)
	default:
		return raw
	}
}

// CooldownActive reports whether the service is still within its scaling cooldown:
// true when lastScaled is set and less than Cooldown before now, so the caller
// must skip scaling this tick. A zero lastScaled (never scaled) is never in
// cooldown.
func (s ServiceScaling) CooldownActive(lastScaled, now time.Time) bool {
	if lastScaled.IsZero() {
		return false
	}
	return now.Sub(lastScaled) < s.Cooldown
}

// NewestVersionedDepth returns the backlog of the newest versioned queue for base
// among depths (queue name -> message count), and whether any versioned queue
// matched. When several versions exist - as during a rollout - the newest version
// (NewestVersion) is the one the current PRIMARY workers consume, so its depth is
// what should drive scaling.
func NewestVersionedDepth(depths map[string]int64, base string) (int64, bool) {
	byVersion := versionedDepths(depths, base)
	if len(byVersion) == 0 {
		return 0, false
	}
	versions := make([]string, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	return byVersion[NewestVersion(versions)], true
}
