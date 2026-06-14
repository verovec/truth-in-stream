package workerlifecycle

import (
	"fmt"
	"sort"
	"time"
)

// RetirableVersions returns the non-current versions of base whose queues are
// fully drained (zero backlog), so the task sets pinned to them may be deleted.
// The current version (NewestVersion) is never retirable - it is what the live
// PRIMARY consumes. With fewer than two versions present there is nothing to
// retire and the result is empty. The result is sorted for a stable caller log.
func RetirableVersions(depths map[string]int64, base string) []string {
	byVersion := versionedDepths(depths, base)
	if len(byVersion) < 2 {
		return nil
	}
	versions := make([]string, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	current := NewestVersion(versions)

	retirable := make([]string, 0, len(byVersion)-1)
	for v, depth := range byVersion {
		if v != current && depth == 0 {
			retirable = append(retirable, v)
		}
	}
	sort.Strings(retirable)
	return retirable
}

// TaskSet is the subset of an ECS task set the cleanup decision needs.
type TaskSet struct {
	ID string
	// Version is the queue version the task set's workers consume, read from its
	// task definition's RABBITMQ_QUEUE_VERSIONS; empty when it could not be
	// determined.
	Version string
	// CreatedAt is when the task set was created.
	CreatedAt time.Time
	// RunningCount is the task set's current running task count.
	RunningCount int
}

// PrimaryTaskSet describes the live PRIMARY task set a retirement decision is made
// against.
type PrimaryTaskSet struct {
	Version      string
	CreatedAt    time.Time
	RunningCount int
}

// RetirePolicy bounds the cleanup decision with the time guards that keep a
// rollout safe.
type RetirePolicy struct {
	// MaxAge is how long the PRIMARY task set must have been serving before any
	// different-version task set is drained and deleted, so traffic has settled on
	// the new version before the old one is torn down.
	MaxAge time.Duration
	// SameVersionMinAge is the minimum age before a same-version-as-PRIMARY task
	// set (a superseded rolling replacement) is deleted; no drain wait is needed
	// because its queue is the live one.
	SameVersionMinAge time.Duration
	// ZombieMinAge is the minimum age before a task set with zero running tasks is
	// deleted regardless of version or drain state, reclaiming a set that never
	// came up.
	ZombieMinAge time.Duration
}

// SafeToRetire reports whether the PRIMARY is healthy enough to permit retiring
// any non-PRIMARY task set: its running count must have caught up to the desired
// count (or the service is scaled to zero). Retiring while the PRIMARY is still
// coming up could drop the fleet below capacity mid-rollout, so the caller must
// preserve every non-PRIMARY task set until this returns true.
func SafeToRetire(primaryRunning, desiredCount int) bool {
	if desiredCount <= 0 {
		return true
	}
	return primaryRunning >= desiredCount
}

// RetireReason returns a reason and true when the non-PRIMARY task set ts should
// be deleted, given the live PRIMARY, the set of drained (retirable) versions,
// the time guards, and the current time. It encodes four independent paths, in
// priority order:
//
//   - orphan: ts has no determinable version - nothing maps it to a queue to
//     drain, so it is always eligible.
//   - same-version: ts shares the PRIMARY's version (a superseded rolling
//     replacement) and is older than SameVersionMinAge - no drain wait needed.
//   - drained: ts's version is retirable (its queues are empty) and the PRIMARY
//     has served longer than MaxAge.
//   - zombie: ts has no running tasks and is older than ZombieMinAge.
//
// It returns false when none apply, so the caller keeps the task set. The caller
// must first gate on SafeToRetire; RetireReason assumes the PRIMARY is healthy.
// Every age-based path requires a non-zero creation time. A missing timestamp
// (an anomalous or partial describe) must never read as an epoch-old age and
// delete a task set that may have just launched; only the orphan path, which is
// unambiguous, fires without an age check.
func RetireReason(ts TaskSet, primary PrimaryTaskSet, retirable map[string]bool, policy RetirePolicy, now time.Time) (string, bool) {
	switch {
	case ts.Version == "":
		return "orphan task set: no queue version on its task definition", true
	case ts.Version == primary.Version && !ts.CreatedAt.IsZero() && now.Sub(ts.CreatedAt) >= policy.SameVersionMinAge:
		return fmt.Sprintf("superseded same-version (%s) task set", ts.Version), true
	case ts.Version != primary.Version && retirable[ts.Version] && !primary.CreatedAt.IsZero() && now.Sub(primary.CreatedAt) >= policy.MaxAge:
		return fmt.Sprintf("version %s drained and PRIMARY older than %s", ts.Version, policy.MaxAge), true
	case ts.RunningCount == 0 && !ts.CreatedAt.IsZero() && now.Sub(ts.CreatedAt) >= policy.ZombieMinAge:
		return fmt.Sprintf("zombie task set: no running tasks for %s", policy.ZombieMinAge), true
	default:
		return "", false
	}
}
